package relevo

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// serverHaltQuotingTheMarker is a halt reason a server can send verbatim: the
// builder's own failure lines reach the client as view.Halt, and a builder whose
// output mentions the unreachable episode's marker produces this text.
const serverHaltQuotingTheMarker = "zen unreachable for 31m0s; round 1 may still be running there"

// TestNeedsYouReasonReplacesABrokenHalt pins that a halt of a different episode
// is told, not deduped against the one already on the binding.
//
// A broken view halts with the fallback reason -- advice the laptop cannot
// follow, since a remote binding refuses a rebind. The server's next tick
// answers needs_you with the real reason, and the per-round dedup dropped it:
// status and `wait` kept showing the fallback and nobody was ever told what
// actually broke.
func TestNeedsYouReasonReplacesABrokenHalt(t *testing.T) {
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
		t.Fatalf("Reconcile on the broken view: %v", err)
	}
	if got.RemoteHaltKind != store.HaltKindBroken {
		t.Fatalf("RemoteHaltKind = %q, want %q on a broken view's halt", got.RemoteHaltKind, store.HaltKindBroken)
	}
	if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1 for the break: %+v", len(halts), halts)
	}

	const reason = "the reviewer needs a human: the patch touches the schema"
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: reason}
	got, err = reconcile(t, at(rt, time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile on the needs_you view: %v", err)
	}

	if got.Halt != reason {
		t.Errorf("Halt = %q, want the server's own reason %q", got.Halt, reason)
	}
	if got.RemoteHaltKind != "" {
		t.Errorf("RemoteHaltKind = %q, want empty: a needs_you halt names no episode of this file", got.RemoteHaltKind)
	}
	halts := haltEntriesFor(t, rt, b.Name)
	if len(halts) != 2 {
		t.Fatalf("halt entries = %d, want 2: the break's and the reason's: %+v", len(halts), halts)
	}
	if halts[1].Note != reason {
		t.Errorf("new entry note = %q, want the reason the MasterMind has to read", halts[1].Note)
	}
}

// TestSameReasonHaltsOnceAcrossViews pins the dedup still holds: only a change of
// episode re-notifies. The same reason twice is one observation, told once.
func TestSameReasonHaltsOnceAcrossViews(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	const reason = "the reviewer needs a human: the patch touches the schema"
	fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: reason}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := b
	var err error
	for i := 1; i <= 3; i++ {
		got, err = reconcile(t, at(rt, time.Duration(i)*time.Minute), got)
		if err != nil {
			t.Fatalf("Reconcile poll %d: %v", i, err)
		}
		if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 1 {
			t.Fatalf("poll %d: halt entries = %d, want 1 across three polls of the same reason", i, len(halts))
		}
	}
}

// TestSwitchableBreakFlapQueuesNoHalt pins the wire half of the same rule: a
// server whose break it is about to retry reports running, and a round that
// flaps between the two never reaches the laptop as a halt at all.
//
// Mutation check: drop the !bindingSwitchable(b) guard in RoundStateOf and the
// broken window arrives here as a fallback halt with an entry.
func TestSwitchableBreakFlapQueuesNoHalt(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	// The server's own binding, broken the way a failed switch leaves it: the
	// round is open, the candidate and the round clock are the ones it had, and
	// the state word says broken. bindingSwitchable says the next tick retries
	// it, so the view it serves is running.
	server := store.Binding{
		Name:             "api",
		State:            store.StateBroken,
		Round:            1,
		BuilderCandidate: "claude/first/m",
		RoundStartedAt:   baseTime.Add(-time.Minute),
		Builder:          store.Endpoint{PID: 4242},
		Serve:            &store.ServeFacts{},
	}
	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}
	view := ServedView(server, entries, "", "")
	if view.RoundState != remote.RoundRunning {
		t.Fatalf("served round state = %q, want %q: the server retries this break itself", view.RoundState, remote.RoundRunning)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{
		RoundState:   view.RoundState,
		Halt:         view.Halt,
		StalledSince: view.StalledSince,
	}, roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n"))}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := b
	var err error
	for i := 1; i <= 4; i++ {
		got, err = reconcile(t, at(rt, time.Duration(i)*time.Minute), got)
		if err != nil {
			t.Fatalf("Reconcile poll %d: %v", i, err)
		}
		if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 0 {
			t.Fatalf("poll %d: halt entries = %d, want none for a break the server retries", i, len(halts))
		}
		if got.State == store.StateNeedsYou {
			t.Fatalf("poll %d: State = %q, want active: nothing here waits on a human", i, got.State)
		}
	}
}

// TestServerHaltQuotingTheMarkerStaysPut pins that the unreachable episode is
// recognised by its own field and not by its text. The needs-you clear runs on
// every view that arrives, so a halt whose text quotes the marker was cleared by
// the first answering view -- which took the server's reason away, answered with
// the server's reason instead, and re-halting every poll after that.
//
// The binding is left on the server's halt, and the server's halt is told once:
// two polls, one entry, the same shape a non-quoting server halt already had.
func TestServerHaltQuotingTheMarkerStaysPut(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{
		RoundState: remote.RoundNeedsYou,
		Halt:       serverHaltQuotingTheMarker,
	}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := b
	var err error
	for i := 1; i <= 2; i++ {
		got, err = reconcile(t, rt, got)
		if err != nil {
			t.Fatalf("Reconcile poll %d: %v", i, err)
		}
		if got.State != store.StateNeedsYou {
			t.Fatalf("poll %d: State = %q, want %q", i, got.State, store.StateNeedsYou)
		}
		if got.Halt != serverHaltQuotingTheMarker {
			t.Fatalf("poll %d: Halt = %q, want the server's reason left in place", i, got.Halt)
		}
		if got.RemoteHaltKind != "" {
			t.Fatalf("poll %d: RemoteHaltKind = %q, want empty: this halt is the server's", i, got.RemoteHaltKind)
		}
	}

	if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 1 {
		t.Errorf("halt entries = %d, want exactly 1 across two polls of the same halt", len(halts))
	}
}

// TestUnreachableHaltStillCarriesItsKind pins the write half: the halt this file
// opens is the one that records the kind, so the clear path can tell it from a
// halt the server sent.
//
// Mutation target: drop the stamp in applyRemoteUnreachable and this reads empty,
// and with the predicate on the field the unreachable episode stops clearing at
// all -- the binding stays NEEDS YOU on a round a human can watch move.
func TestUnreachableHaltStillCarriesItsKind(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := unreachableHaltedBinding(t, rt, b)
	if got.RemoteHaltKind != store.HaltKindUnreachable {
		t.Fatalf("RemoteHaltKind = %q, want %q on the halt this file writes", got.RemoteHaltKind, store.HaltKindUnreachable)
	}
}

// TestUnreachableKindNotStampedOnADedupedHalt pins the stamp's own guard: a
// halt the notification key dedupes tells nobody, so the kind must not attach
// itself to the reason that already went out. Stamped outside the guard it named
// the episode of a halt that was never the one on the binding, and the answering
// view then cleared a halt this arm did not write.
//
// The server's own needs-you halt notifies first and stamps the round; the
// server then goes silent past the budget and grace, which is exactly the
// unreachable arm's trigger, but the round has already told its MasterMind.
//
// Mutation target: move the kind stamp out of the HaltNotifiedRound guard in
// haltBindingKind and this reads unreachable for a halt the server sent.
func TestUnreachableKindNotStampedOnADedupedHalt(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	const reason = "the plan needs a human: the scope widened"
	fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: reason}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.HaltNotifiedRound != got.Round {
		t.Fatalf("HaltNotifiedRound = %d, want %d as the fixture", got.HaltNotifiedRound, got.Round)
	}

	// The server is silent past the budget and grace: the unreachable arm fires,
	// and the round's own halt dedupes it.
	fr.getBindingResp = remote.BindingView{}
	fr.getBindingErr = fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, err = reconcile(t, at(rt, 31*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile past the grace: %v", err)
	}

	if got.RemoteHaltKind != "" {
		t.Errorf("RemoteHaltKind = %q, want empty: the unreachable halt was deduped, so it named no episode", got.RemoteHaltKind)
	}
	if got.Halt != reason {
		t.Errorf("Halt = %q, want the server's reason %q untouched", got.Halt, reason)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want needs_you", got.State)
	}
}

// TestStaleUnreachableKindDoesNotWipeALaterServerHalt pins the round-advance
// clear: a halt notified for the old round says nothing about the new one, and
// the kind that named its episode goes with it. Left behind, the kind answered
// for a halt the new round never had, and the first running view cleared the
// server's own reason off the binding -- which then re-halted and requeued the
// same reason on every poll.
//
// The scenario is the two staleness paths in one sequence: the unreachable
// episode ends at the round close, the new round gets the server's own needs-you
// reason, and a running view leaves that reason standing with the one entry it
// was told in.
func TestStaleUnreachableKindDoesNotWipeALaterServerHalt(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := unreachableHaltedBinding(t, rt, b)
	if err := st.Save(got); err != nil {
		t.Fatal(err)
	}

	// The server answers with round 1 closed: the close advances the binding to
	// round 2, and the halt -- with the kind that named its episode -- has no
	// say over the round that follows.
	rt2 := Runtime{Store: st, Remote: stoppedRoundRemote("killed", ""), Now: func() time.Time { return baseTime }}
	if _, err := SyncRemote(context.Background(), rt2); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2: the closed round advances", got.Round)
	}
	if got.Halt != "" || got.RemoteHaltKind != "" {
		t.Fatalf("halt %q kind %q, want both cleared with the round they were about", got.Halt, got.RemoteHaltKind)
	}

	// Round 2 carries the server's own reason.
	const reason = "the reviewer needs a human: the patch touches the schema"
	fr3 := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: reason}}
	rt3 := Runtime{Store: st, Remote: fr3, Now: func() time.Time { return baseTime }}

	got, err = reconcile(t, rt3, got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou || got.Halt != reason {
		t.Fatalf("binding = %q/%q, want needs_you on the server's reason", got.State, got.Halt)
	}
	if got.RemoteHaltKind != "" {
		t.Errorf("RemoteHaltKind = %q, want empty: this halt names no unreachable episode", got.RemoteHaltKind)
	}

	// A running view answers nothing the server's reason asks, so the reason
	// stands -- and because it stands, the round is already notified and the
	// same needs-you view does not queue a second entry.
	fr3.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	fr3.roundFileFromResp = io.NopCloser(strings.NewReader("builder log line 1\n"))
	got, err = reconcile(t, at(rt3, time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile on the running view: %v", err)
	}
	if got.State != store.StateNeedsYou || got.Halt != reason {
		t.Errorf("binding = %q/%q, want the server's reason left standing on a running round", got.State, got.Halt)
	}

	fr3.getBindingResp = remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: reason}
	if _, err := reconcile(t, at(rt3, 2*time.Minute), got); err != nil {
		t.Fatalf("Reconcile on the repeat needs-you view: %v", err)
	}
	if halts := haltEntriesFor(t, rt3, "api"); len(halts) != 2 {
		t.Errorf("halt entries = %d, want 2: the unreachable episode's one and the server's reason's one, with no wipe-requeue", len(halts))
	}
}

// TestClearedUnreachableHaltDropsItsKind pins the kind is cleared with the text.
// Left behind, the field would answer unreachableHalted for a binding with no
// halt at all, and a server that drops out again would clear nothing on the
// tick that ought to reopen the episode.
func TestClearedUnreachableHaltDropsItsKind(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := unreachableHaltedBinding(t, rt, b)

	fr.getBindingErr = nil
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	fr.roundFileFromResp = io.NopCloser(strings.NewReader("builder log line 1\n"))

	got, err := reconcile(t, at(rt, 31*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.RemoteHaltKind != "" {
		t.Errorf("RemoteHaltKind = %q, want empty once the halt is cleared", got.RemoteHaltKind)
	}
}
