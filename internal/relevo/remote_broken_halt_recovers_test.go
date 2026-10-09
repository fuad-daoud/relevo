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
	if got.RemoteHaltKind != store.HaltKindBroken {
		t.Fatalf("RemoteHaltKind = %q, want %q: the halt came from a broken view", got.RemoteHaltKind, store.HaltKindBroken)
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

// TestServerReasonBrokenHaltClearsOnRunningView pins the recovery for a break the
// server named. The clear is the kind the halt carries, not the text it happens
// to hold: the fallback-only scoping cleared only a break a server with nothing
// to say produced, and every current server names its reason -- so the binding
// sat on NEEDS YOU until the server replaced it, and `wait` answered needs-you on
// a round a human could watch move.
//
// Mutation target: match the fallback text (the old brokenHaltedOnRunning) and
// this stays needs-you.
func TestServerReasonBrokenHaltClearsOnRunningView(t *testing.T) {
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
	if got.State != store.StateActive {
		t.Errorf("State = %q, want active: a running round is the builder the halt said was gone", got.State)
	}
	if got.Halt != "" || !got.HaltAt.IsZero() {
		t.Errorf("Halt = %q at %v, want both cleared", got.Halt, got.HaltAt)
	}
	if got.RemoteHaltKind != "" {
		t.Errorf("RemoteHaltKind = %q, want empty once the break is cleared", got.RemoteHaltKind)
	}
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

// TestBrokenHaltClearsOnAQueuedView pins the other half of the new bound: a
// queued round ends the break too. The server is answering and holding the round
// to run it, which contradicts the claim the break made -- that nothing would
// run it -- so the binding relays to the queue rather than sitting on NEEDS YOU
// while its round waits for a slot.
func TestBrokenHaltClearsOnAQueuedView(t *testing.T) {
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
	if got.State != store.StateActive {
		t.Errorf("State = %q, want active: a queued round is the server holding the round to run it", got.State)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want the break cleared", got.Halt)
	}
}

// TestUnrelatedRemoteHaltSurvivesARunningView pins that the new clear is the
// one fallback this file writes and not every halt on a remote binding. A 404
// and a revoked key say something a running round has not contradicted -- the
// binding the server does not know and the key the owner revoked are still
// what they were -- and a running view answers neither question.
func TestUnrelatedRemoteHaltSurvivesARunningView(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		halt string
	}{
		{"binding not found", "api: zen: binding not found on the server (deleted there, or this machine's key changed)"},
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
