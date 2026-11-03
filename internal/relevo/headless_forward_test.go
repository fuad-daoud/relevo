package relevo

import (
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// forwardHeadless is sentHeadless with the round forwarded to a burst node:
// the builder scripted exited and the report marker present, so without the
// forward guard the marker close would fire on this tick.
func forwardHeadless(t *testing.T, fr *fakeRunner, phase string) (Runtime, store.Binding) {
	t.Helper()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	fr.script(b.Builder.PID, false)
	b.Burst = &store.BurstFacts{Forward: &store.Forward{
		Node:        "node-1",
		NodeBinding: "deadbeef-webshop",
		Round:       1,
		Phase:       phase,
	}}
	return rt, b
}

// TestReconcileHeadlessLeavesForwardedRound pins the forward guard: while
// the node owns the round the local tick leaves it alone, marker or no
// marker, so the round stays open and the builder is never even probed.
//
// Mutation check: drop the guard and this fails on the round closing.
func TestReconcileHeadlessLeavesForwardedRound(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"sending", "running"} {
		fr := newFakeRunner()
		rt, b := forwardHeadless(t, fr, phase)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("%s: Reconcile: %v", phase, err)
		}
		if !store.SameBinding(b, got) {
			t.Errorf("%s: forwarded binding changed: before=%+v after=%+v", phase, b, got)
		}
		if got.Round != 1 {
			t.Errorf("%s: Round = %d, want 1 (the marker must not close a forwarded round)", phase, got.Round)
		}
		if fr.aliveCalls != 0 {
			t.Errorf("%s: aliveCalls = %d, want 0 (no liveness probe on a forwarded round)", phase, fr.aliveCalls)
		}
	}
}

// TestReconcileHeadlessClosesClosedForward pins the guard's edge: a closed
// forward waits only for the node's ack, so the local tick runs on and the
// present marker closes the round instead of holding the next one back.
func TestReconcileHeadlessClosesClosedForward(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := forwardHeadless(t, fr, "closed")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 {
		t.Fatalf("round=%d pid=%d; want the marker to close round 1 despite the unacked forward", got.Round, got.Builder.PID)
	}
}
