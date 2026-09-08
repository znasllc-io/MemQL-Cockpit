package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/znasllc-io/memql-cockpit/internal/config"
)

// The round-trip deadline must NOT cover the sign-in.
//
// EnsureValidToken is the interactive one here -- a person typed this command,
// so an expired token opens a browser. That is human-paced: find the window,
// pick an account, approve. Twenty seconds is a perfectly good ceiling for
// "the cluster did not answer" and a terrible one for "the human has not
// finished logging in", and the failure would be a sign-in that dies partway
// through with a deadline error naming nothing the person did wrong.
func TestFetchDoesNotPutTheSignInUnderTheRoundTripDeadline(t *testing.T) {
	var seen context.Context
	restore := ensureToken
	t.Cleanup(func() { ensureToken = restore })
	sentinel := errors.New("stop here")
	ensureToken = func(ctx context.Context, _ config.ClusterConfig) (string, error) {
		seen = ctx
		return "", sentinel
	}

	_, err := Fetch(context.Background(), config.ClusterConfig{
		Name: "acme", Endpoint: "https://api.acme.test",
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Fetch returned %v, want the stub's error", err)
	}
	if seen == nil {
		t.Fatal("the sign-in was never reached")
	}
	if deadline, ok := seen.Deadline(); ok {
		t.Errorf("the sign-in ran under a deadline %s away; it must inherit the caller's context",
			time.Until(deadline).Round(time.Second))
	}
}

// The network round trip, by contrast, MUST be bounded -- a person is waiting
// at a prompt, and a cluster that has not answered is a fact worth printing
// rather than something to keep waiting for.
func TestFetchBoundsTheRoundTripAfterTheSignIn(t *testing.T) {
	restore := ensureToken
	t.Cleanup(func() { ensureToken = restore })
	ensureToken = func(context.Context, config.ClusterConfig) (string, error) {
		return "token", nil
	}

	start := time.Now()
	_, err := Fetch(context.Background(), config.ClusterConfig{
		Name: "acme", Endpoint: "https://api.invalid.test:59999",
	})
	if err == nil {
		t.Fatal("Fetch succeeded against an unreachable endpoint")
	}
	if elapsed := time.Since(start); elapsed > callTimeout+10*time.Second {
		t.Errorf("Fetch took %s; the round trip is not bounded by callTimeout (%s)", elapsed, callTimeout)
	}
}
