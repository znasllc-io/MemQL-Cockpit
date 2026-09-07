# The machine scanner, the probe and shared machines -- cockpit half, Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** This machine tells the cluster what it IS (a hardware inventory), proves what it can DO (a versioned probe suite with measured figures), says whether it will serve ANYONE (`inference.serve`), and installs the speech and image runtimes on consent.

**Architecture:** A new `internal/worker/hardware` package scans presence facts through build-tagged probes into a plain struct, so `Scan` and `Class` are pure functions that fixture-test on any runner -- the same shape `internal/worker/models`'s floor and `internal/worker/inference`'s `Decide` already use. A new `internal/worker/probe` package runs a version-pinned suite against the local runtime through an exported `modelcall` client, reporting each case as a figure or the reason it has none. `inference.serve` rides the EXISTING `capability_descriptor_json` Register field. The speech and image runtimes reuse the consent-and-print shape `setup --inference` established.

**Tech Stack:** Go, single module. No new dependencies. `golang.org/x/sys/unix` (already required) for sysctls.

**Spec:** the engine repository's `docs/superpowers/specs/2026-09-07-machine-scanner-and-shared-machines-design.md`, sections 3 (D1, D3, D6, D7, D8) and 7 (tasks 1-4). Engine epic znasllc-io/memql#5146.

## Global Constraints

- **The wire for this half does not exist.** At the pin (`5c4f6ae9`) `component/grpc/worker.proto` has no `Register.hardware`, no `Heartbeat.hardware` and no `ModelProbeStart/Progress/End`. Neither does engine `main` (`0cd415a41`). Build the producer, name ONE seam per field, write no dispatch arm for a message type that is not there -- the rule PR #392 set for `ModelPull*`.
- **`capability_descriptor_json` is the exception and IS live.** The engine's `ParseCapabilityDescriptor` unmarshals into a struct without `DisallowUnknownFields`, so an unknown key survives validation; its `AsMap()` then drops it, so the graph does not see it at the pin. The cockpit ALREADY sends one such key (`displays`). `inferenceServe` joins it.
- **`CapabilityDescriptorSchemaVersion` STAYS 1.** The engine rejects any other value outright (`unsupported schemaVersion %d`). Bumping it fails every registration on the fleet.
- **Presence facts only.** No serial numbers, no user names, no paths, no hostname. Asserted by a test over the field set, not by review.
- **Every capability defaults to ABSENT** (`internal/worker/models` package doc). A probe that cannot establish a fact claims nothing.
- **The sentences are the product.** Every refusal and every verdict is asserted verbatim in a test, per `internal/worker/inference`'s package doc. Rewording is a deliberate diff.
- **Nothing runs sudo.** A command needing it is PRINTED and labelled as the person's to run.
- Wrap prose at `inferenceWrapWidth` (76). Sentence case. No ALL-CAPS in operator copy.

---

### Task 1: The hardware inventory and the machine class (#397)

**Files:**
- Create: `internal/worker/hardware/hardware.go` (the struct, `Scan`, the `Probe` seam)
- Create: `internal/worker/hardware/class.go` (`Class`, `Usable`)
- Create: `internal/worker/hardware/runtimes.go` (the six runtime version probes)
- Create: `internal/worker/hardware/probe_darwin.go`, `probe_linux.go`, `probe_other.go`
- Create: `internal/worker/hardware/hardware_test.go`, `class_test.go`
- Create: `internal/worker/hardware_cmd.go` (`memql worker hardware`)
- Create: `internal/worker/hardware_cmd_test.go`
- Modify: `internal/worker/connect.go` (the Register seam, the Heartbeat seam)
- Modify: `internal/worker/loop.go` (the tenth-beat cadence)
- Modify: `internal/worker/cli.go` (dispatch + usage)
- Test: `internal/worker/connect_register_test.go`, `internal/worker/loop_test.go`

**Interfaces:**
- Produces: `hardware.Inventory` (JSON-tagged: `chip`, `memoryBytes`, `gpu{name,vramBytes,backend}`, `cpuCores`, `osVersion`, `diskFreeBytes`, `runtimes[]{name,version}`, `reportedAt`); `hardware.Scan(ctx, Probe) Inventory`; `hardware.Local(ctx) Inventory`; `hardware.Class(Inventory) string` returning `"16"|"24"|"32"|"64"|"128"|"unsupported"`.
- Consumed by: Task 2's `memql worker probe` header line, Plan B Task 1's recommended set.

- [ ] **Step 1: Write the failing shape-and-privacy test**

`internal/worker/hardware/hardware_test.go`:

```go
// The field set IS the privacy review. A field added without a line
// here is a field nobody decided to send.
func TestInventoryFieldSet(t *testing.T) {
	raw, err := json.Marshal(hardware.Inventory{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"chip", "cpuCores", "diskFreeBytes", "memoryBytes",
		"osVersion", "reportedAt", "runtimes"}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("inventory field set = %v, want %v (gpu is omitempty)", keys, want)
	}
}

// Nothing that names a PERSON or a PLACE on this machine may appear.
func TestInventoryCarriesNothingPersonal(t *testing.T) {
	inv := hardware.Scan(context.Background(), hardware.Probe{
		GOOS: "darwin", GOARCH: "arm64",
		Chip:        func() (string, error) { return "Apple M3 Max", nil },
		MemoryBytes: func() (uint64, error) { return 64 << 30, nil },
		OSVersion:   func() (string, error) { return "15.1", nil },
		CPUCores:    func() int { return 16 },
		GPUs:        func() ([]models.GPU, error) { return nil, nil },
		Backend:     func() string { return "metal" },
		DiskFree:    func() uint64 { return 400 << 30 },
		Runtimes:    func(context.Context) []hardware.Runtime { return nil },
		Now:         func() time.Time { return time.Unix(0, 0).UTC() },
	})
	raw, _ := json.Marshal(inv)
	body := string(raw)
	host, _ := os.Hostname()
	for _, forbidden := range []string{host, os.Getenv("USER"), os.Getenv("HOME"), "/Users/", "/home/"} {
		if forbidden == "" {
			continue
		}
		if strings.Contains(body, forbidden) {
			t.Fatalf("inventory leaked %q: %s", forbidden, body)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/worker/hardware/ -run TestInventory -v`
Expected: FAIL, `no required module provides package .../internal/worker/hardware`.

- [ ] **Step 3: Write `hardware.go`**

```go
// Package hardware is what this machine IS, as presence facts.
//
// The cluster cannot see any of it: the cockpit dials out from behind
// NAT and the engine reads what Register carries. A machine with no
// model pulled yet is exactly the machine this exists for -- deriving a
// class from the models advertised would tell the cluster nothing about
// the machine that has none.
//
// PRESENCE FACTS ONLY. No serial numbers, no user names, no paths, no
// hostname. That is not a style rule: this payload is stored on a
// registration row that the owner's whole cluster can read, and a field
// added here is a field every reader of that row now has. The field set
// is asserted by a test rather than reviewed.
//
// SCAN IS A PURE FUNCTION OF Probe, for the reason inference.Decide is
// a pure function of Host: every platform question is answered by a
// build-tagged file into the struct, so the RULES fixture-test on any CI
// runner instead of only on whichever one happens to match.
package hardware

// Backend names how a GPU is reached.
const (
	BackendMetal = "metal"
	BackendCUDA  = "cuda"
	BackendROCm  = "rocm"
	BackendNone  = "none"
)

// GPU is the graphics device the cluster is told about. Absent entirely
// when the machine has none the probes can vouch for -- an omitted gpu
// and a gpu with backend "none" are different claims, and only the
// first is honest about a machine nothing was found on.
type GPU struct {
	Name      string `json:"name"`
	VRAMBytes uint64 `json:"vramBytes"`
	Backend   string `json:"backend"`
}

// Runtime is one model runtime present on this machine, with the
// version it reported. A runtime whose version could not be read is
// still reported, with an empty version: "present, version unknown" and
// "absent" send an operator to different places.
type Runtime struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Inventory is the whole payload.
type Inventory struct {
	Chip          string    `json:"chip"`
	MemoryBytes   uint64    `json:"memoryBytes"`
	GPU           *GPU      `json:"gpu,omitempty"`
	CPUCores      int       `json:"cpuCores"`
	OSVersion     string    `json:"osVersion"`
	DiskFreeBytes uint64    `json:"diskFreeBytes"`
	Runtimes      []Runtime `json:"runtimes"`
	ReportedAt    time.Time `json:"reportedAt"`
}

// Probe is everything Scan is allowed to know. Each fact-returning
// probe may fail, and a failure means "could not establish", which is
// never the same as establishing a zero -- an unreadable memory size is
// reported as absent so the engine reads "this cockpit could not tell"
// rather than "this machine has no memory".
type Probe struct {
	GOOS, GOARCH string
	Chip         func() (string, error)
	MemoryBytes  func() (uint64, error)
	OSVersion    func() (string, error)
	CPUCores     func() int
	GPUs         func() ([]models.GPU, error)
	Backend      func() string
	DiskFree     func() uint64
	Runtimes     func(context.Context) []Runtime
	Now          func() time.Time
}

func Scan(ctx context.Context, p Probe) Inventory {
	inv := Inventory{Runtimes: []Runtime{}}
	if p.Now != nil {
		inv.ReportedAt = p.Now().UTC()
	}
	if p.Chip != nil {
		if v, err := p.Chip(); err == nil {
			inv.Chip = v
		}
	}
	if p.MemoryBytes != nil {
		if v, err := p.MemoryBytes(); err == nil {
			inv.MemoryBytes = v
		}
	}
	if p.OSVersion != nil {
		if v, err := p.OSVersion(); err == nil {
			inv.OSVersion = v
		}
	}
	if p.CPUCores != nil {
		inv.CPUCores = p.CPUCores()
	}
	if p.DiskFree != nil {
		inv.DiskFreeBytes = p.DiskFree()
	}
	if p.Runtimes != nil {
		if rts := p.Runtimes(ctx); len(rts) > 0 {
			inv.Runtimes = rts
		}
	}
	inv.GPU = scanGPU(p)
	return inv
}

// scanGPU picks the LARGEST device, the same choice the floor makes,
// so the two packages never disagree about which card this machine is.
func scanGPU(p Probe) *GPU {
	backend := BackendNone
	if p.Backend != nil {
		backend = p.Backend()
	}
	// Apple Silicon has no discrete device to enumerate: the GPU is the
	// chip and its memory is the unified pool. Reporting it with the
	// unified figure is what lets the engine's class function read one
	// field on both platforms.
	if backend == BackendMetal {
		mem := uint64(0)
		if p.MemoryBytes != nil {
			if v, err := p.MemoryBytes(); err == nil {
				mem = v
			}
		}
		name := "Apple GPU"
		if p.Chip != nil {
			if v, err := p.Chip(); err == nil && strings.TrimSpace(v) != "" {
				name = v
			}
		}
		return &GPU{Name: name, VRAMBytes: mem, Backend: BackendMetal}
	}
	if p.GPUs == nil {
		return nil
	}
	gpus, err := p.GPUs()
	if err != nil || len(gpus) == 0 {
		return nil
	}
	best := gpus[0]
	for _, g := range gpus {
		if g.VRAMBytes > best.VRAMBytes {
			best = g
		}
	}
	return &GPU{Name: best.Name, VRAMBytes: best.VRAMBytes, Backend: backend}
}
```

- [ ] **Step 4: Run the tests to green**

Run: `go test ./internal/worker/hardware/ -run TestInventory -v`
Expected: PASS both.

- [ ] **Step 5: Write the failing class table test**

`internal/worker/hardware/class_test.go`. Usable memory is 75% of unified on Metal and VRAM on a discrete GPU (record D2); the class is the largest of 16/24/32/64/128 not exceeding it.

```go
func TestClass(t *testing.T) {
	gb := func(n uint64) uint64 { return n << 30 }
	for _, tc := range []struct {
		name string
		inv  hardware.Inventory
		want string
	}{
		{"metal 16 GB -> usable 12 -> unsupported",
			hardware.Inventory{MemoryBytes: gb(16), GPU: &hardware.GPU{Backend: "metal", VRAMBytes: gb(16)}}, "unsupported"},
		{"metal 24 GB -> usable 18 -> 16",
			hardware.Inventory{MemoryBytes: gb(24), GPU: &hardware.GPU{Backend: "metal", VRAMBytes: gb(24)}}, "16"},
		{"metal 32 GB -> usable 24 -> 24",
			hardware.Inventory{MemoryBytes: gb(32), GPU: &hardware.GPU{Backend: "metal", VRAMBytes: gb(32)}}, "24"},
		{"metal 64 GB -> usable 48 -> 32",
			hardware.Inventory{MemoryBytes: gb(64), GPU: &hardware.GPU{Backend: "metal", VRAMBytes: gb(64)}}, "32"},
		{"metal 128 GB -> usable 96 -> 64",
			hardware.Inventory{MemoryBytes: gb(128), GPU: &hardware.GPU{Backend: "metal", VRAMBytes: gb(128)}}, "64"},
		{"metal 192 GB -> usable 144 -> 128",
			hardware.Inventory{MemoryBytes: gb(192), GPU: &hardware.GPU{Backend: "metal", VRAMBytes: gb(192)}}, "128"},
		{"cuda 24 GB VRAM -> usable 24 -> 24",
			hardware.Inventory{MemoryBytes: gb(128), GPU: &hardware.GPU{Backend: "cuda", VRAMBytes: gb(24)}}, "24"},
		{"cuda 8 GB VRAM -> usable 8 -> unsupported",
			hardware.Inventory{MemoryBytes: gb(64), GPU: &hardware.GPU{Backend: "cuda", VRAMBytes: gb(8)}}, "unsupported"},
		{"no gpu -> unsupported", hardware.Inventory{MemoryBytes: gb(64)}, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hardware.Class(tc.inv); got != tc.want {
				t.Fatalf("Class = %q, want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 6: Run it, watch it fail, write `class.go`**

Run: `go test ./internal/worker/hardware/ -run TestClass -v` -> FAIL "undefined: hardware.Class".

```go
// ClassUnsupported is a machine below the smallest class. It is NOT
// the same verdict as the hardware floor's: a Linux box with 8 GB of
// VRAM meets the floor (it can serve a 9B model) and is still below
// class 16. The floor decides whether this machine serves at all; the
// class decides only which set is RECOMMENDED for it, and the two are
// kept apart so a machine that works is never told it does not.
const ClassUnsupported = "unsupported"

// classes ascending. Fixed by the design record (D2).
var classes = []uint64{16, 24, 32, 64, 128}

// UsableBytes is the memory a model actually gets: 75 percent of the
// unified pool on Metal, because the OS and the display keep the rest,
// and the whole of VRAM on a discrete card, because nothing else is in
// it.
func UsableBytes(inv Inventory) uint64 {
	if inv.GPU == nil {
		return 0
	}
	if inv.GPU.Backend == BackendMetal {
		return inv.GPU.VRAMBytes / 4 * 3
	}
	return inv.GPU.VRAMBytes
}

// Class is the largest class not exceeding usable memory.
func Class(inv Inventory) string {
	usableGB := UsableBytes(inv) >> 30
	out := ClassUnsupported
	for _, c := range classes {
		if usableGB >= c {
			out = strconv.FormatUint(c, 10)
		}
	}
	return out
}
```

- [ ] **Step 7: Run it to green, then write the platform probes**

Run: `go test ./internal/worker/hardware/ -v` -> PASS.

`probe_darwin.go` reads `machdep.cpu.brand_string` for the chip, `hw.memsize`, `kern.osproductversion`, `hw.ncpu`, backend `metal` when `hw.optional.arm64`; `probe_linux.go` reads `/proc/cpuinfo` `model name` for the chip, `/proc/meminfo` `MemTotal`, `/etc/os-release` `PRETTY_NAME` for the OS version, `runtime.NumCPU()`, reuses `models.GPU` discovery through an exported seam, and picks `cuda` when nvidia-smi answered and `rocm` when the amdgpu node did; `probe_other.go` returns a bare inventory. Disk free comes from `unix.Statfs` on the runtime's model directory. `Local(ctx)` assembles the platform probe and calls `Scan`.

**Note:** `models.linuxGPUs` and `models.darwinAppleSilicon` are unexported. Export the minimum: add `models.DetectGPUs() ([]GPU, error)` and `models.GPUBackend() string` in a new `models/gpu.go`, delegating to the existing platform files. Do NOT duplicate the probes -- two GPU readers drift, and the failure is a class computed from a card the floor did not see.

- [ ] **Step 8: Write the runtime version probes**

`runtimes.go`. Six names, in the record's order: `ollama`, `mlx`, `whispercpp`, `kokoro`, `mflux`, `docker`. Each is `present + version`, each bounded by a 5-second context (`probeTimeout`, matching the two sibling packages), and **each reports absence silently** -- a machine without MLX is the ordinary case, not a fault.

- `ollama`: `GET <base>/api/version` -> `{"version":"..."}`. HTTP rather than the CLI, because a Linux machine set up by this cockpit runs Ollama in a CONTAINER and has no `ollama` on PATH -- the same trap `inference.Pull` documents.
- `docker`: `docker version --format {{.Server.Version}}`, absent when the daemon does not answer.
- `kokoro`: `GET <kokoroBase>/health`, then `/v1/models`; version from the payload when it names one, empty when it does not.
- `mlx`, `mflux`, `whispercpp`: `python3 -c "import mlx; print(mlx.__version__)"`, `mflux-generate --version`, `whisper-cli --version` respectively -- each behind `exec.LookPath` so a missing binary costs nothing.

Table-test `parseRuntimeVersion` on captured fixture strings; do not test the shell-outs.

- [ ] **Step 9: Write the failing cadence test**

`internal/worker/loop_test.go`:

```go
// Register carries the inventory, then every tenth beat. At the 15s
// default that is a refresh every two and a half minutes -- the
// record's "within minutes" -- without a shell-out on every beat.
func TestHardwareOnBeat(t *testing.T) {
	for beat, want := range map[int]bool{
		1: false, 2: false, 9: false, 10: true, 11: false, 20: true, 100: true,
	} {
		if got := hardwareOnBeat(beat); got != want {
			t.Fatalf("hardwareOnBeat(%d) = %v, want %v", beat, got, want)
		}
	}
}
```

- [ ] **Step 10: Implement the cadence and the two seams**

`loop.go`: give `heartbeatLoop` a beat counter, and call the cached scanner only when `hardwareOnBeat(n)`.

```go
// hardwareRefreshBeats is how often the inventory is re-scanned onto
// the beat. The scan shells out (nvidia-smi, docker version), so it
// does not belong on every one.
const hardwareRefreshBeats = 10

func hardwareOnBeat(beat int) bool { return beat%hardwareRefreshBeats == 0 }
```

`connect.go` grows the two seams, each ONE line, each named so landing the proto is a mapping:

```go
// registerHardware is the seam for Register.hardware (memql#5146,
// record D1). The field does not exist at the pin -- not on it, and
// not on engine main -- so the inventory is computed, asserted and
// printed by `memql worker hardware`, and this is the single line that
// changes when the proto lands:
//
//	register.Hardware = hardwareToProto(inv)
//
// Nothing is invented in the meantime: the payload is NOT smuggled
// into capability_descriptor_json, because the engine's AsMap() drops
// unknown keys and a shape defined in the wrong place is one the
// engine's own field would then have to disagree with.
func registerHardware(register *memqlv1.Register, inv hardware.Inventory) { _ = inv }
```

- [ ] **Step 11: Write `memql worker hardware`**

`internal/worker/hardware_cmd.go`. The surface an operator reads when the class is wrong. It prints what would be reported, then the class and what follows from it, in the established two-column shape:

```
This machine reports the following to the cluster.

  Chip         Apple M3 Max
  Memory       64 GB unified
  GPU          Apple M3 Max, 64 GB, metal
  CPU cores    16
  OS           macOS 15.1
  Disk free    412 GB
  Runtimes     ollama 0.13.0, docker 27.3.1

  Class        32 -- 48 GB usable, 75% of 64 GB unified

Nothing above names you, this machine, or any path on it.
```

A machine below every class prints the class line as `unsupported -- 6 GB usable, below the smallest class (16 GB)` and then the sentence `This machine can still serve models it has room for; the class only decides which set is recommended for it.` -- the distinction Step 6's comment makes, said to the person.

- [ ] **Step 12: Commit**

```bash
git add internal/worker/hardware internal/worker/hardware_cmd.go internal/worker/hardware_cmd_test.go \
        internal/worker/models/gpu.go internal/worker/connect.go internal/worker/loop.go \
        internal/worker/cli.go internal/worker/loop_test.go internal/worker/connect_register_test.go
git commit -m "hardware: what this machine is, as presence facts and a class"
```

---

### Task 2: The probe suite and the version gate (#398)

**Files:**
- Create: `internal/worker/probe/probe.go` (`Suite`, `Run`, `Figure`, the version gate)
- Create: `internal/worker/probe/cases.go` (the five schemas, the three tools, the two throughput prompts)
- Create: `internal/worker/probe/probe_test.go`, `cases_test.go`
- Create: `internal/worker/probe_cmd.go`, `probe_cmd_test.go`
- Modify: `internal/worker/modelcall/runtime.go`, `session.go` (export the client seam)
- Modify: `internal/worker/cli.go`

**Interfaces:**
- Consumes: `models.Info` from Task 1's sibling package; `hardware.Class` for the header.
- Produces: `probe.SuiteVersion` (int, currently 1); `probe.Run(ctx, Request) (Report, error)`; `probe.Figure{Name, Value, Unit, AbsentReason}`; `modelcall.NewClient(models.Info, *http.Client, func(string) string) modelcall.Client` and the exported `modelcall.Client` interface.

- [ ] **Step 1: Export the runtime client from `modelcall`**

`clientFor` is already the one place that knows how to build an Ollama or an OpenAI-compatible client from a `models.Info`. Rename the unexported `client` interface to `Client`, export `NewClient` with `clientFor`'s body, and leave `clientFor` calling it. Two callers must not learn two ways to reach a runtime -- the failure is a probe measuring a model through a path the serving code does not use, which measures the wrong thing.

Run: `go test ./internal/worker/modelcall/` -> PASS (pure rename).

- [ ] **Step 2: Write the failing version-gate test**

```go
func TestRunRefusesAnUnknownSuiteVersion(t *testing.T) {
	_, err := probe.Run(context.Background(), probe.Request{SuiteVersion: probe.SuiteVersion + 1})
	if err == nil {
		t.Fatal("want a refusal for a suite version this cockpit does not know")
	}
	want := "this machine knows probe suite version 1; the cluster asked for version 2. " +
		"Update the cockpit on this machine and run the probe again."
	if err.Error() != want {
		t.Fatalf("refusal =\n%q\nwant\n%q", err.Error(), want)
	}
}
```

The gate is one-directional on purpose: an OLDER suite version is also refused, because a figure measured by a suite the cluster is not asking about would be filed under the version it did ask for.

- [ ] **Step 3: Run it, watch it fail, write the gate and the report shape**

Run: `go test ./internal/worker/probe/ -run TestRunRefuses -v` -> FAIL (no package).

```go
// SuiteVersion is the ONLY suite this cockpit can run. It is a pin,
// not a floor: figures are filed by (machine, model, suiteVersion) on
// the engine, so running a different suite under this number would
// file measurements of one thing as measurements of another. Bump it
// in the same commit that changes any case.
const SuiteVersion = 1

// Figure is one measurement, or the reason there is none.
//
// THE TWO ARE NEVER THE SAME SHAPE ON SCREEN OR ON THE WIRE. A case
// that could not run and a case that scored zero are opposite facts --
// the first says nothing about the model, the second says the model
// failed -- and a renderer that showed "0" for both would rank a
// working model below a broken one.
type Figure struct {
	Name  string
	Value float64
	Unit  string
	// AbsentReason is a complete sentence when there is no value.
	// Non-empty means Value is meaningless and must not be read.
	AbsentReason string
}

func (f Figure) Measured() bool { return f.AbsentReason == "" }
```

- [ ] **Step 4: Write the failing known-good / known-bad case tests**

`cases_test.go` drives the five structured cases against a fake `Client` that returns a canned body per case, asserting validity 1.0 for the good fixtures and 0.0 for the bad ones, and asserting that a body which is not JSON at all is a FAILED case rather than an absent figure -- the model answered, and prose where a schema was asked for is exactly the failure this measures.

```go
func TestStructuredValidity(t *testing.T) {
	good := fakeClient{reply: map[string]string{
		"triage":  `{"severity":"high","summary":"disk full","owner":"platform"}`,
		"intake":  `{"fields":[{"name":"email","value":"a@b.c"}],"complete":true}`,
		"symptom": `{"symptoms":["timeout"],"confidence":0.8}`,
		"factory": `{"decision":"rebuild","reason":"stale cache"}`,
		"healing": `{"patches":[{"path":"a.go","hunk":"@@"}]}`,
	}}
	rep, err := probe.Run(context.Background(), probe.Request{
		SuiteVersion: probe.SuiteVersion, Model: "m", Client: good,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := rep.Figure("structured_validity")
	if !f.Measured() || f.Value != 1.0 {
		t.Fatalf("structured_validity = %+v, want 1.0 measured", f)
	}
}

func TestStructuredValidityCountsProseAsAFailure(t *testing.T) {
	rep, _ := probe.Run(context.Background(), probe.Request{
		SuiteVersion: probe.SuiteVersion, Model: "m",
		Client: fakeClient{always: "Sure! Here is the triage result."},
	})
	f := rep.Figure("structured_validity")
	if !f.Measured() || f.Value != 0.0 {
		t.Fatalf("prose must MEASURE as 0, not go absent: %+v", f)
	}
}
```

- [ ] **Step 5: Write `cases.go`**

Five schemas drawn from the platform's own prompts (triage, intake, symptom, factory decision, healing patches), three tool definitions, two throughput prompts at 8K and 32K.

**Each case carries its schema JSON AND a hand-written `check func([]byte) error` against that exact schema.** No general JSON Schema validator is pulled in, and that is a decision rather than a shortcut: the schemas here are fixed and ours, a draft-2020 validator is a dependency and a second thing to be wrong, and a partial validator that checked only `required` would score a model that returned the right keys with the wrong types as valid -- which is precisely the failure the structured figure exists to catch.

The 8K and 32K prompts are GENERATED from a fixed seed rather than embedded, so the file does not carry 40 KB of filler and every run measures the same tokens.

- [ ] **Step 6: Write the failing timeout test**

```go
// A runaway case ends and reports an ABSENT figure naming the timeout.
// The whole run must survive it: one wedged case is not a reason to
// lose the four that measured cleanly.
func TestCaseTimeoutReportsAnAbsentFigureAndTheRunContinues(t *testing.T) {
	rep, err := probe.Run(context.Background(), probe.Request{
		SuiteVersion: probe.SuiteVersion, Model: "m",
		CaseTimeout: 20 * time.Millisecond,
		Client:      fakeClient{hangFor: time.Second, hangOn: "throughput_8k"},
	})
	if err != nil {
		t.Fatalf("one wedged case must not fail the run: %v", err)
	}
	f := rep.Figure("throughput_8k")
	if f.Measured() {
		t.Fatal("a case that timed out must not report a value")
	}
	want := "the 8K throughput case did not answer within 20ms and was ended."
	if f.AbsentReason != want {
		t.Fatalf("AbsentReason = %q, want %q", f.AbsentReason, want)
	}
	if !rep.Figure("structured_validity").Measured() {
		t.Fatal("the cases that did answer must still be reported")
	}
}
```

- [ ] **Step 7: Implement `Run` with the per-case supervisor**

Each case runs under `context.WithTimeout(ctx, req.CaseTimeout)`, defaulting to `DefaultCaseTimeout = 120 * time.Second`. This is the appsession supervisor's discipline (`session.go:475`) applied per case rather than per session: a probe is a sequence of independent measurements, and a whole-run ceiling would lose every figure to the last case that hung.

- [ ] **Step 8: Write `memql worker probe`**

`internal/worker/probe_cmd.go`. Heading before work, per case, then the figures. The absent reason hangs UNDER the figure column rather than beside it, so a measured row and an unmeasured row cannot be scanned as the same kind of thing:

```
Measuring qwen3.5:9b with probe suite 1.

  Structured output   5 schemas
  Tool calls          3 definitions
  Throughput          8K and 32K prompts

  structured validity      1.00      5 of 5 schemas held
  tool call correctness    0.67      2 of 3 definitions
  throughput 8K            41.2 tokens/sec
  time to first token 8K   0.42 sec
  throughput 32K           --
      the 32K case did not answer within 2m0s and was ended.

These figures rank this machine; they gate nothing. A model that fails
a case is still offered for the calls it advertises.
```

The closing sentence is D4 said to the person, and it is not decoration: an operator who reads a 0.67 and believes the model has been switched off will go looking for a switch that does not exist.

- [ ] **Step 9: Run everything, commit**

Run: `go test ./internal/worker/probe/ ./internal/worker/ ./internal/worker/modelcall/ -v` -> PASS.

```bash
git add internal/worker/probe internal/worker/probe_cmd.go internal/worker/probe_cmd_test.go \
        internal/worker/modelcall internal/worker/cli.go
git commit -m "probe: a versioned suite, measured figures, and the reason a case has none"
```

---

### Task 3: `inference.serve` as the machine's sharing consent (#399)

**Files:**
- Modify: `internal/worker/tools/policy.go` (`InferencePolicy`, `InferenceServe()`)
- Modify: `internal/worker/tools/capabilities.go` (`InferenceServe` on the descriptor)
- Modify: `internal/worker/connect.go` (thread the policy verdict into the descriptor)
- Modify: `internal/worker/cli.go` (the SIGHUP log line)
- Test: `internal/worker/tools/policy_test.go`, `internal/worker/tools/capabilities_test.go`, `internal/worker/connect_register_test.go`

**Interfaces:**
- Produces: `tools.ServeOwner = "owner"`, `tools.ServeCluster = "cluster"`, `(*Policy).InferenceServe() string`; `tools.CapabilityDescriptorJSONFor(serve string) (string, error)`.

- [ ] **Step 1: Write the failing default test**

```go
// Default OWNER. A machine that says nothing has not consented to
// serving anyone else's prompt, and the absent-is-permissive reading
// would hand a stranger somebody's GPU on an upgrade.
func TestInferenceServeDefaultsToOwner(t *testing.T) {
	if got := tools.DefaultPolicy().InferenceServe(); got != tools.ServeOwner {
		t.Fatalf("default inference.serve = %q, want %q", got, tools.ServeOwner)
	}
	p, err := tools.LoadPolicy(writeTemp(t, "shell:\n  allow: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.InferenceServe(); got != tools.ServeOwner {
		t.Fatalf("absent inference.serve = %q, want %q", got, tools.ServeOwner)
	}
}

// An unrecognised value is OWNER, not an error and not cluster. A
// typo'd policy file must not widen a permission, and it must not stop
// a worker starting either.
func TestInferenceServeUnknownValueIsOwner(t *testing.T) {
	p, _ := tools.LoadPolicy(writeTemp(t, "inference:\n  serve: everyone\n"))
	if got := p.InferenceServe(); got != tools.ServeOwner {
		t.Fatalf("inference.serve: everyone -> %q, want %q", got, tools.ServeOwner)
	}
}
```

- [ ] **Step 2: Run it, watch it fail, implement**

Run: `go test ./internal/worker/tools/ -run TestInferenceServe -v` -> FAIL "p.InferenceServe undefined".

```go
// InferencePolicy is this machine's answer to "who may this GPU
// serve". It is a plain string rather than a bool because the engine's
// half is a two-consent handshake (record D6) whose other side is the
// owner's `sharing.mode`, and both sides spell the same two words.
type InferencePolicy struct {
	Serve string `yaml:"serve"`
}

const (
	// ServeOwner: only this machine's owner. The default, and what an
	// unrecognised value falls back to.
	ServeOwner = "owner"
	// ServeCluster: anyone in the cluster, subject to the OWNER's
	// separate grant on the machine page. This half alone grants
	// nothing -- a machine cannot consent on its owner's behalf, which
	// is the same rule that keeps sharedInference off the cockpit.
	ServeCluster = "cluster"
)
```

- [ ] **Step 3: Write the failing descriptor test**

```go
// The key rides the EXISTING capability_descriptor_json field. It
// survives the engine's ParseCapabilityDescriptor (which tolerates
// unknown keys) and is dropped by its AsMap(), so the graph does not
// see it at the pin -- the same one-way situation the model label
// attributes are in, and the reason TestWireContract exists.
func TestCapabilityDescriptorCarriesInferenceServe(t *testing.T) {
	raw, err := tools.CapabilityDescriptorJSONFor(tools.ServeCluster)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["inferenceServe"] != "cluster" {
		t.Fatalf("inferenceServe = %v, want cluster", got["inferenceServe"])
	}
	// THE VERSION MUST NOT MOVE. The engine admits exactly 1 and
	// refuses anything else by number; bumping it here fails every
	// registration on the fleet with "unsupported schemaVersion".
	if got["schemaVersion"] != float64(1) {
		t.Fatalf("schemaVersion = %v, want 1 -- see component/worker/capability_descriptor.go", got["schemaVersion"])
	}
}
```

- [ ] **Step 4: Implement, thread it through `buildRegister`, log the SIGHUP change**

`buildRegister` gains the policy verdict; the SIGHUP handler in `cli.go` compares old and new and logs on a change:

```
worker: inference.serve changed from owner to cluster; it takes effect on the next reconnect
```

The log says "on the next reconnect" and no reconnect is forced. A reconnect here would interrupt a running model call to change a field that does not affect it, which is the trade `RequestImmediateReadvertise` already refuses to make for labels.

- [ ] **Step 5: Run and commit**

```bash
go test ./internal/worker/... && git add -A && \
git commit -m "policy: inference.serve, the machine's half of the sharing consent"
```

---

### Task 4: The speech and image runtimes, consented and idempotent (#400)

**Files:**
- Create: `internal/worker/inference/runtimes.go` (`RuntimeSpec`, `DecideRuntime`, the probes)
- Create: `internal/worker/inference/runtimes_test.go`
- Create: `internal/worker/runtime_cmd.go`, `runtime_cmd_test.go`
- Modify: `internal/worker/models/models.go` (`RuntimeLabel` carries a version)
- Modify: `internal/worker/cli.go` (`--runtime kokoro|image` mode, usage)
- Test: `internal/worker/models/models_test.go`

**Interfaces:**
- Consumes: `hardware.Runtime` version probes from Task 1.
- Produces: `inference.RuntimeKokoro = "kokoro"`, `inference.RuntimeImage = "image"`, `inference.DecideRuntime(Host, name) RuntimePlan`.

- [ ] **Step 1: Write the failing flag-collision test**

`--runtime` already means `docker|native` alongside `--inference`. The two meanings must not silently blur.

```go
func TestRuntimeFlagRefusesTheInferenceCombination(t *testing.T) {
	out := runSetup(t, "--inference", "--runtime", "kokoro")
	want := "--runtime kokoro installs a speech runtime and --runtime docker or native " +
		"chooses how Ollama runs. They are different questions, so they are different " +
		"commands: run memql worker setup --runtime kokoro on its own."
	if !strings.Contains(out, want) {
		t.Fatalf("output did not carry the refusal:\n%s", out)
	}
	if code := lastExit(t); code != SetupExitUsage {
		t.Fatalf("exit = %d, want %d", code, SetupExitUsage)
	}
}
```

- [ ] **Step 2: Run it, watch it fail, implement the mode split in `cli.go`**

`--runtime kokoro` and `--runtime image` WITHOUT `--inference` enter the runtime-install flow. With `--inference` they are refused by name. `--runtime docker|native` without `--inference` is refused symmetrically. Refused rather than ignored, for the reason `applyRuntimeFlag` already gives about `--runtime docker` on a Mac.

- [ ] **Step 3: Write the failing non-interactive refusal test**

```go
// Exit 3 and nothing installed -- the acceptance criterion, and the
// same code and shape --inference already uses for a consent it cannot
// obtain.
func TestRuntimeInstallRefusesUnderNonInteractive(t *testing.T) {
	out, code := runRuntimeSetup(t, "kokoro", "--non-interactive")
	if code != SetupExitRefused {
		t.Fatalf("exit = %d, want %d", code, SetupExitRefused)
	}
	if !strings.Contains(out, "Nothing was installed: --non-interactive cannot answer that question.") {
		t.Fatalf("output:\n%s", out)
	}
}
```

- [ ] **Step 4: Write the failing idempotence test**

```go
// A second run installs nothing and says why in the present tense. A
// re-run that printed the install commands again would read as a
// machine that had lost the runtime.
func TestRuntimeInstallIsIdempotent(t *testing.T) {
	out, code := runRuntimeSetupWith(t, "kokoro", presentAt("http://127.0.0.1:8880", "0.2.4"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "Kokoro is already running at http://127.0.0.1:8880 (0.2.4). Nothing to install.") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(out, "docker run") {
		t.Fatal("an install command was printed at a machine that already has the runtime")
	}
}
```

- [ ] **Step 5: Implement `DecideRuntime`**

Same shape as `Decide`: a pure function of `Host` plus the runtime name, returning commands, a refusal, or "already present". Two runtimes:

- **kokoro** -- a speech runtime, reached over HTTP at `KokoroBaseURL` (`MEMQL_KOKORO_HOST`, default `http://127.0.0.1:8880`). Docker on BOTH platforms, and that is a deliberate divergence from D1's "no Docker on macOS": D1 forbids it because a container cannot reach the Mac's GPU and an LLM would then serve from the CPU, which is what the hardware floor exists to prevent. An 82M-parameter speech model is not that call -- it is real-time on a CPU -- so the reason does not apply and the refusal it justifies would block a runtime that works. The `Note` says exactly this, because a reader who knows D1 will otherwise read this as a bug.
- **image** -- NOT a service to install. Ollama's image generation is a capability of the runtime this machine may already have, so the flow is: refuse if Ollama is absent (naming `setup --inference`), else probe for an image-capable model, else name the pull. A refusal here states what was OBSERVED (`the Ollama at %s reports no image generation capability`) rather than asserting which platforms the vendor offers it on -- a claim this cockpit cannot verify and would be wrong about on the next release.

Never start a background service unasked: the commands are printed, the question covers all of them at once, and `nil` consent is a no.

- [ ] **Step 6: Write the failing version-label test**

```go
// The label VALUE becomes the version. Absent version -> empty value,
// exactly as today, so a runtime whose version could not be read is
// still advertised.
func TestRuntimeLabelCarriesTheVersion(t *testing.T) {
	inv := models.Inventory{
		Floor:           models.FloorVerdict{Met: true},
		Models:          []models.Info{{ID: "m", Kind: models.KindOllama, Allowed: true}},
		RuntimeVersions: map[string]string{models.KindOllama: "0.13.0"},
	}
	if got := inv.Labels()["runtime:ollama"]; got != "0.13.0" {
		t.Fatalf("runtime:ollama = %q, want 0.13.0", got)
	}
}
```

- [ ] **Step 7: Implement, and note the reconnect**

`Inventory.RuntimeVersions` is filled by discovery. The label value changing rewrites every machine's fingerprint ONCE on rollout -- the cost D8 accepts, the same one `params` and `quant` already charged. Say so in the commit message so a fleet-wide reconnect on deploy is not read as an incident.

The label appears only after the runtime ANSWERS a probe: `runtime:kokoro` is written from the health probe's response, never from the fact that an install command was run.

- [ ] **Step 8: Run everything and commit**

```bash
go test ./... && git add -A && \
git commit -m "runtimes: kokoro and image, asked for and idempotent, advertised with a version"
```

---

## Self-Review

**Spec coverage.** D1 -> Task 1 (the field list is copied verbatim, including `reportedAt`). D3 -> Task 2 (the suite, the three families, the supervisor). D6's cockpit half -> Task 3 (`capabilityDescriptor.inferenceServe`; the engine's `registration.sharing` and `setWorkerSharing` are the engine's and are NOT in this plan). D7 -> Task 4. D8 -> Task 4 Step 7 (the reconnect) and Task 1 Step 10 (the cadence). D2, D4, D5 are engine-side and out of this half by the record's own delivery section.

**Gaps stated rather than hidden.** The engine computes `machineClass` in `component/memql/fleet_class.go`; this plan computes it AGAIN in the cockpit because `setup --inference` must choose a set before any engine round trip, on a machine that may not be paired. The two must agree, so Task 1 Step 6 copies the record's rule verbatim and the table test is the contract. When the engine's function lands, the cockpit's is the fallback, not the second opinion.

**Placeholders.** None: every step carries the code or the exact sentence.

**Type consistency.** `hardware.Inventory` is the name in Tasks 1, 2 and 4. `models.GPU` (existing) feeds `hardware.GPU` (new) through `scanGPU` -- deliberately two types, because the floor's GPU has no backend and adding one there would change a struct three probes write. `probe.Figure` is used only in Task 2. `tools.ServeOwner` / `ServeCluster` are used only in Task 3.
