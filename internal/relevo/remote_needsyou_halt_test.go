package relevo

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// needsYouWithClosingRound is the wire shape that used to double-halt: the
// server says the round both closed and needs a human. A round that closed and
// then halted again reports exactly this, and the client must collect the close
// first and let the catch-up own whatever it decides -- including the decision
// to halt for its own reason.
//
// The bundle fetch fails, so the catch-up takes the bundle-failure path: an
// increment and, at the budget, a halt.
func needsYouWithClosingRound() *fakeRemote {
	return &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState:  remote.RoundNeedsYou,
			ClosedRound: 1,
			Halt:        "server-side: builder wedged on a dialog",
		},
		roundFileFunc: func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleErr: &client.HTTPError{Status: 500, Body: remote.ErrorBody{Code: "boom", Message: "bundle read failed"}},
	}
}

// remoteNeedsYouCatchUpCountsHaltEntries reads the binding's log and counts the
// halt entries, which is what "exactly one halt" means observably: the log is
// the record both a human and every later reader sees.
func remoteNeedsYouCatchUpCountsHaltEntries(t *testing.T, st *store.Store, name string) int {
	t.Helper()
	entries, err := st.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == store.KindHalt && e.Direction == store.DirToMasterMind {
			n++
		}
	}
	return n
}

// TestRemoteNeedsYouCatchUpHaltSingleEntry pins #961's third defect: a
// needs_you view that also names a closed round halts once, and the reason is
// the catch-up's.
//
// The old branch ran applyCatchUp, discarded its `next` whenever it owed no
// settle, and fell through to haltAndSettle on the stale binding with
// view.Halt. So the deciding path's reason was clobbered, a second entry was
// filed for a round already notified, and the failure counters were dropped.
func TestRemoteNeedsYouCatchUpHaltSingleEntry(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	// The tenth bundle failure is the one that halts (catchUpFailureBudget).
	b.RemoteBundleFailures = 9
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := needsYouWithClosingRound()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q", got.State, store.StateNeedsYou)
	}
	// The catch-up's reason survives: view.Halt ("builder wedged on a dialog")
	// must not be what the human is told, because it is not why this client
	// stopped.
	if !strings.Contains(got.Halt, "cannot fetch round bundle 1 from zen") {
		t.Errorf("Halt = %q, want the catch-up's bundle reason", got.Halt)
	}
	if strings.Contains(got.Halt, "wedged on a dialog") {
		t.Errorf("Halt = %q, want no trace of view.Halt clobbering the reason", got.Halt)
	}

	if n := remoteNeedsYouCatchUpCountsHaltEntries(t, st, "api"); n != 1 {
		t.Errorf("halt entries = %d, want exactly 1: the catch-up already halted this round", n)
	}
	if got.RemoteBundleFailures != 10 {
		t.Errorf("RemoteBundleFailures = %d, want 10: the increment must not be dropped", got.RemoteBundleFailures)
	}
}

// TestRemoteBundleFailuresAccumulateThroughNeedsYou pins the counter half of
// the same defect across ticks: a needs_you-with-close view whose bundle keeps
// failing accumulates one failure per tick and reaches the budget, rather than
// halting on the first tick on view.Halt with the count discarded.
func TestRemoteBundleFailuresAccumulateThroughNeedsYou(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := needsYouWithClosingRound()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := b
	var err error
	for i := 1; i <= 10; i++ {
		got, err = reconcile(t, rt, got)
		if err != nil {
			t.Fatalf("Reconcile iteration %d: %v", i, err)
		}
		if got.RemoteBundleFailures != i {
			t.Fatalf("iteration %d: RemoteBundleFailures = %d, want %d: the count must accumulate through the needs_you path",
				i, got.RemoteBundleFailures, i)
		}
		if i < 10 && got.State == store.StateNeedsYou {
			t.Fatalf("iteration %d: halted before the budget (%d)", i, got.RemoteBundleFailures)
		}
	}

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q at the budget", got.State, store.StateNeedsYou)
	}
	if !strings.Contains(got.Halt, "cannot fetch round bundle 1 from zen") {
		t.Fatalf("Halt = %q, want the bundle reason", got.Halt)
	}
	if n := remoteNeedsYouCatchUpCountsHaltEntries(t, st, "api"); n != 1 {
		t.Errorf("halt entries = %d, want exactly 1 across ten ticks", n)
	}
}