package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRemoteBrokenViewHaltsWithServerReason pins the broken arm of an answered
// view: a server holding a round whose builder is gone is NEEDS YOU here, halted
// with the server's own reason and owing exactly one entry.
//
// A broken view used to fall through to the default arm, which reports a
// payload and returns. The binding stayed Active, `relevo wait` sat on a round
// no process would finish, and the MasterMind heard nothing: the state word
// said broken and the halt said nothing, because the break was never read.
func TestRemoteBrokenViewHaltsWithServerReason(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{
		RoundState: remote.RoundBroken,
		Halt:       "builder claude; switching to codex failed: exit status 1",
	}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q: a round with no builder is waiting on a human", got.State, store.StateNeedsYou)
	}
	// The server's reason, verbatim: it is the only account of the break.
	if got.Halt != "builder claude; switching to codex failed: exit status 1" {
		t.Errorf("Halt = %q, want the server's reason", got.Halt)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if r := WaitOutcome(got, entries, 1, func(string, int) string { return "" }); r.Code != WaitNeedsYou {
		t.Errorf("WaitOutcome = %+v, want %v", r, WaitNeedsYou)
	} else if !strings.Contains(r.Line, "switching to codex failed") {
		t.Errorf("wait line = %q, want the server's reason on it", r.Line)
	}

	if n := remoteNeedsYouCatchUpCountsHaltEntries(t, st, "api"); n != 1 {
		t.Errorf("halt entries = %d, want exactly 1", n)
	}
}

// TestRemoteBrokenHaltsOnceAcrossPolls pins the dedup half: a broken round is
// polled every tick, and each poll is the same observation. Only the first owes
// the MasterMind an entry.
func TestRemoteBrokenHaltsOnceAcrossPolls(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{
		RoundState: remote.RoundBroken,
		Halt:       "builder claude; switching to codex failed",
	}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := b
	var err error
	for i := 1; i <= 3; i++ {
		got, err = reconcile(t, rt, got)
		if err != nil {
			t.Fatalf("Reconcile iteration %d: %v", i, err)
		}
		if got.State != store.StateNeedsYou {
			t.Fatalf("iteration %d: State = %q, want %q", i, got.State, store.StateNeedsYou)
		}
		if n := remoteNeedsYouCatchUpCountsHaltEntries(t, st, "api"); n != 1 {
			t.Fatalf("iteration %d: halt entries = %d, want 1: one observation, one entry", i, n)
		}
	}
}

// TestRemoteBrokenWithoutServerReasonStillHalts pins the fallback: a server that
// ships no reason must not produce a halt whose payload says nothing. The entry
// a human reads is the whole notification, so an empty one is silence.
func TestRemoteBrokenWithoutServerReasonStillHalts(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundBroken}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q", got.State, store.StateNeedsYou)
	}
	if !strings.Contains(got.Halt, "builder for this round is gone") {
		t.Errorf("Halt = %q, want a reason naming what is wrong", got.Halt)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var halt *store.LogEntry
	for i, e := range entries {
		if e.Kind == store.KindHalt && e.Direction == store.DirToMasterMind {
			halt = &entries[i]
			break
		}
	}
	if halt == nil {
		t.Fatalf("no halt entry in %+v", entries)
	}
	if strings.TrimSpace(halt.Note) == "" || strings.TrimSpace(halt.Payload) == "" {
		t.Errorf("halt entry = %+v, want a note and a payload with something in them", *halt)
	}
}
