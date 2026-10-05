package relevo

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// brokenHaltedBinding halts b on the server's broken-round view and returns it
// once it is NEEDS YOU for the break, so the clearing tests start from a binding
// that is actually halted rather than one that clears trivially.
func brokenHaltedBinding(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you on a broken round as the fixture", got.State)
	}
	if got.RemoteHaltKind != "" {
		t.Fatalf("RemoteHaltKind = %q, want empty: a broken halt is the server's, not an unreachable episode", got.RemoteHaltKind)
	}
	return got
}

// TestBrokenFallbackHaltClearsOnRunningView pins the recovery: a binding left on
// the fallback text -- the break the server reported with no reason of its own --
// goes Active when the server answers with a running round.
//
// The fallback is what made the halt unrecoverable. Rebind is refused at both
// ends while the binding is broken, so a human could not clear it by re-binding,
// and nothing else ever did: the running arm read the round and left the halt,
// so the laptop sat on NEEDS YOU quoting a break the server had already recovered
// from, and `wait` answered needs_you on a round a human could watch move.
func TestBrokenFallbackHaltClearsOnRunningView(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundBroken}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := brokenHaltedBinding(t, rt, b)
	if !strings.Contains(got.Halt, "builder for this round is gone") {
		t.Fatalf("Halt = %q, want the broken fallback as the fixture", got.Halt)
	}

	// The server has the builder back, and says the round is running.
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	fr.roundFileFromResp = io.NopCloser(strings.NewReader("builder log line 1\n"))

	got, err := reconcile(t, at(rt, time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want active: a running round is the builder the halt said was gone", got.State)
	}
	if got.Halt != "" || !got.HaltAt.IsZero() {
		t.Errorf("Halt = %q at %v, want both cleared", got.Halt, got.HaltAt)
	}
	// The stamp goes with the text, so a break that happens again halts and
	// queues its own entry rather than staying silent for a notified round.
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0 so the next break is its own episode", got.HaltNotifiedRound)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if r := WaitOutcome(got, entries, 1, func(string, int) string { return "" }); r.Code == WaitNeedsYou {
		t.Errorf("WaitOutcome = %+v, want no needs_you on a visibly running round", r)
	}
}

// TestServerReasonBrokenHaltSurvivesARunningView pins that the clear is this
// file's fallback halt and not every halt a broken view produces. A named reason
// is the server's statement about the round and the server owns it: a running
// round says the builder is back, not that the switch that failed is no longer a
// thing anyone needs told. The server replaces it, and until it does the entry
// the MasterMind reads stands.
//
// Mutation target: match the broken view's state rather than its fallback text
// and this clears a reason the server sent.
func TestServerReasonBrokenHaltSurvivesARunningView(t *testing.T) {
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

	got := brokenHaltedBinding(t, rt, b)

	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	fr.roundFileFromResp = io.NopCloser(strings.NewReader("builder log line 1\n"))

	got, err := reconcile(t, at(rt, time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want needs_you: the server's own reason is the server's to replace", got.State)
	}
	if got.Halt != "builder claude; switching to codex failed: exit status 1" {
		t.Errorf("Halt = %q, want the server's reason left in place", got.Halt)
	}
}

// TestBrokenHaltSurvivesAQueuedView pins the bound of the new clear: a queued
// round is not proof the builder came back. The server is answering and holding
// the round, but nothing is running it, which is what the break said -- so the
// halt stands until a running view or the server's own reason replaces it.
//
// Mutation target: widen the running condition to the whole of
// unreachableHaltDisproven and this clears a break on a round that is still
// stuck.
func TestBrokenHaltSurvivesAQueuedView(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundBroken}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := brokenHaltedBinding(t, rt, b)

	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundQueued}

	got, err := reconcile(t, at(rt, time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want needs_you: a queued round is not proof the builder came back", got.State)
	}
	if got.Halt == "" {
		t.Error("Halt = \"\", want the break left in place")
	}
}

// TestUnrelatedRemoteHaltSurvivesARunningView pins that the new clear is the
// one fallback this file writes and not every halt on a remote binding. A 404 and
// a revoked key say something a running round has not contradicted -- the admin
// who removed the binding and the owner who revoked it are still the ones who
// know -- and a running view answers neither question.
func TestUnrelatedRemoteHaltSurvivesARunningView(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		halt string
	}{
		{"removed by the admin", "api: zen: binding removed by the server admin"},
		{"revoked key", "api: zen: key revoked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := store.New(t.TempDir())
			b := remoteBinding("zen")
			b.State = store.StateNeedsYou
			b.Halt = strings.TrimPrefix(tc.halt, "api: ")
			b.HaltAt = baseTime
			b.HaltNotifiedRound = b.Round
			if err := st.Save(b); err != nil {
				t.Fatal(err)
			}

			fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundRunning}}
			rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

			got, err := reconcile(t, at(rt, time.Minute), b)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if got.State != store.StateNeedsYou {
				t.Errorf("State = %q, want needs_you: a running round contradicts no %s", got.State, tc.name)
			}
			if got.Halt != b.Halt {
				t.Errorf("Halt = %q, want %q left in place", got.Halt, b.Halt)
			}
		})
	}
}
