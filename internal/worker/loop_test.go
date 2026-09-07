package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/znasllc-io/memql-cockpit/internal/worker/models"
)

// fakeModelInventory is what the runner sees instead of Ollama. It also
// counts invalidations, because the re-advertise and the cache drop are
// one API call on purpose: a caller that got only half of it would
// re-register the label set from before the pull.
type fakeModelInventory struct {
	mu            sync.Mutex
	inv           models.Inventory
	invalidations int
}

func (f *fakeModelInventory) Models(ctx context.Context) models.Inventory {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inv
}

func (f *fakeModelInventory) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidations++
}

func (f *fakeModelInventory) serve(inv models.Inventory) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inv = inv
}

func (f *fakeModelInventory) invalidated() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.invalidations
}

// testRunner builds a runner with a clock the test moves by hand. The
// guard it exercises is TWO MINUTES of wall clock; a test that waited it
// out would spend two minutes of every CI run to assert one branch.
func testRunner(inv ModelInventory, now *time.Time) *Runner {
	return &Runner{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		modelsInv:      inv,
		readvertiseNow: make(chan struct{}, 1),
		now:            func() time.Time { return *now },
		closed:         make(chan struct{}),
	}
}

// testConnection is a Connection with no stream under it. Close() is
// safe on one -- the SDK connection checks its own nil receiver -- which
// is what lets the re-advertise decision be asserted without a cluster.
func testConnection(fingerprint string) *Connection {
	return &Connection{ModelFingerprint: fingerprint}
}

func oneModel() models.Inventory {
	return servingInventory(offeredModel("llama3.1:8b", models.Attributes{ContextWindow: 8192, MaxConcurrent: 1}))
}

func twoModels() models.Inventory {
	return servingInventory(
		offeredModel("llama3.1:8b", models.Attributes{ContextWindow: 8192, MaxConcurrent: 1}),
		offeredModel("nomic-embed-text", models.Attributes{ContextWindow: 2048, Embeddings: true, MaxConcurrent: 4}),
	)
}

// TestMaybeReadvertiseModels_TheFloorHoldsWithoutARequest.
//
// A runtime flapping between up and down must not turn this worker into
// one that reconnects forever, which is worse than a stale label.
func TestMaybeReadvertiseModels_TheFloorHoldsWithoutARequest(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	inv := &fakeModelInventory{}
	inv.serve(oneModel())
	r := testRunner(inv, &clock)

	if !r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Fatal("a changed model set on an idle worker must re-advertise")
	}
	clock = clock.Add(30 * time.Second)
	if r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Error("a second re-advertise inside the two-minute floor must be refused")
	}
	clock = clock.Add(modelReadvertiseMinInterval)
	if !r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Error("once the floor has passed, a changed set re-advertises again")
	}
}

// TestMaybeReadvertiseModels_UnchangedLabelsChangeNothing. Discovery
// running again is not news; a model appearing is.
func TestMaybeReadvertiseModels_UnchangedLabelsChangeNothing(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	inv := &fakeModelInventory{}
	inv.serve(oneModel())
	r := testRunner(inv, &clock)

	same := advertisedFingerprint(oneModel().Labels())
	if r.maybeReadvertiseModels(context.Background(), testConnection(same)) {
		t.Error("re-discovering the same set must not spend a reconnect")
	}
}

// TestRequestImmediateReadvertise_BypassesTheFloorExactlyOnce.
//
// Somebody pressed Pull and is watching. Two minutes of a model that does
// not appear reads as a pull that failed, so the request skips the floor
// -- ONCE. If the bypass survived its own use, a flapping runtime plus
// one old request would reconnect this worker on every refresh tick,
// which is the failure the floor exists to prevent.
func TestRequestImmediateReadvertise_BypassesTheFloorExactlyOnce(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	inv := &fakeModelInventory{}
	inv.serve(oneModel())
	r := testRunner(inv, &clock)

	if !r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Fatal("the first re-advertise must happen")
	}
	clock = clock.Add(5 * time.Second)
	if r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Fatal("without a request the floor holds")
	}

	inv.serve(twoModels())
	r.RequestImmediateReadvertise()
	if !r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Error("a pull with somebody watching must re-advertise inside the floor")
	}
	clock = clock.Add(time.Second)
	if r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Error("the bypass is a one-shot: the second call inside the floor must be refused")
	}
}

// TestRequestImmediateReadvertise_DoesNotOverrideBusy.
//
// A reconnect that interrupts a running model call or app session is
// worse than a stale label, and "somebody is watching" is not a reason to
// kill their own in-flight work. The request is not spent by a busy
// return either: the next evaluation, once the work is done, still gets
// its bypass -- otherwise the busiest machines would be the ones that
// waited out the floor.
func TestRequestImmediateReadvertise_DoesNotOverrideBusy(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	inv := &fakeModelInventory{}
	inv.serve(oneModel())
	r := testRunner(inv, &clock)
	// A re-advertise a moment ago, so the floor is live for this test.
	r.lastReadvertise.Store(clock.UnixNano())
	clock = clock.Add(10 * time.Second)

	r.activeCalls.Add(1)
	r.RequestImmediateReadvertise()
	if r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Fatal("a busy worker must not reconnect, watched or not")
	}

	r.activeCalls.Add(-1)
	if !r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Error("the request must survive the busy window and bypass the floor once the work is done")
	}
	if r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Error("and then be spent")
	}
}

// TestRequestImmediateReadvertise_InvalidatesTheInventory.
//
// The two halves are one call because a caller that did only the
// re-advertise would reconnect and re-register the label set from BEFORE
// the pull: the reconnect happens, nothing changes, and the pull reads as
// one that did nothing.
func TestRequestImmediateReadvertise_InvalidatesTheInventory(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	inv := &fakeModelInventory{}
	inv.serve(oneModel())
	r := testRunner(inv, &clock)

	r.RequestImmediateReadvertise()
	if got := inv.invalidated(); got != 1 {
		t.Errorf("inventory invalidations = %d, want 1", got)
	}
}

// TestRequestImmediateReadvertise_CollapsesABurst. `setup --inference`
// pulls a default PAIR of models, and a machine being set up may pull
// several. Each finished pull is one request; they must collapse into one
// evaluation rather than queueing one reconnect per model.
func TestRequestImmediateReadvertise_CollapsesABurst(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	inv := &fakeModelInventory{}
	inv.serve(oneModel())
	r := testRunner(inv, &clock)

	r.RequestImmediateReadvertise()
	r.RequestImmediateReadvertise()
	r.RequestImmediateReadvertise()

	select {
	case <-r.readvertiseNow:
	default:
		t.Fatal("a request must wake the heartbeat loop rather than wait out the refresh ticker")
	}
	select {
	case <-r.readvertiseNow:
		t.Error("a burst of requests must collapse into one wake-up")
	default:
	}
}

// TestRequestImmediateReadvertise_SafeWithoutAModelInventory. A build
// that reports no models at all passes nil, and the pull path must not
// have to know which build it is running in.
func TestRequestImmediateReadvertise_SafeWithoutAModelInventory(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	r := testRunner(nil, &clock)
	r.RequestImmediateReadvertise()

	var nilRunner *Runner
	nilRunner.RequestImmediateReadvertise()

	if r.maybeReadvertiseModels(context.Background(), testConnection("")) {
		t.Error("a runner with no inventory has nothing to re-advertise")
	}
}
