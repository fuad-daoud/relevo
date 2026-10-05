package relevo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// serverHaltQuotingTheMarker is a halt reason a server can send verbatim: the
// builder's own failure lines reach the client as view.Halt, and a builder whose
// output mentions the unreachable episode's marker produces this text.
const serverHaltQuotingTheMarker = "zen unreachable for 31m0s; round 1 may still be running there"

// TestLocalResumeClearsTheHaltItResumed pins the same clear on the local
// resume path, which sets the state word and keeps every halt field.
//
// The key is the part that costs: a resume is the human's answer to the halt, so
// the next halt of the same round is a new thing to tell them about, and the
// stale key dedupes it away.
func TestLocalResumeClearsTheHaltItResumed(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := haltAndSave(rt, &b, "round 1 has run past 30m0s"); err != nil {
		t.Fatalf("first halt: %v", err)
	}
	if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 1 {
		t.Fatalf("halt entries = %d, want 1 before the resume: %+v", len(halts), halts)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}

	if got.Halt != "" || !got.HaltAt.IsZero() || got.HaltNotifiedRound != 0 || got.RemoteHaltKind != "" {
		t.Errorf("resume kept halt %q kind %q key %d, want every field cleared",
			got.Halt, got.RemoteHaltKind, got.HaltNotifiedRound)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want %q after a resume", got.State, store.StateActive)
	}

	// The claim the clear exists for: a second halt of the same round is a new
	// thing to tell the MasterMind about, and it owes its own entry.
	if err := haltAndSave(rt, &got, "round 1 has run past 30m0s again"); err != nil {
		t.Fatalf("second halt: %v", err)
	}
	halts := haltEntriesFor(t, rt, b.Name)
	if len(halts) != 2 {
		t.Fatalf("halt entries = %d, want 2 across a resume: %+v", len(halts), halts)
	}
	if halts[1].Note != "round 1 has run past 30m0s again" {
		t.Errorf("second entry note = %q, want the new reason", halts[1].Note)
	}
}

// haltAndSave halts b through the shared path and saves what comes back, which
// is what every reconcile halt site does with its own binding.
func haltAndSave(rt Runtime, b *store.Binding, reason string) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		next, err := haltAndSettle(context.Background(), rt, tx, *b, b.Name+": "+reason)
		if err != nil {
			return err
		}
		*b = next
		return tx.Save(next)
	})
}

// TestRemoteResumeClearsTheHaltItResumed pins the remote half of the resume
// clear: the server agreed to resume, so the halt that asked for the resume is
// answered, and the per-round key goes with it.
//
// Left stamped, the next halt of the same round dedupes against a reason nobody
// is waiting on any more, and the MasterMind is never told what broke this time.
func TestRemoteResumeClearsTheHaltItResumed(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.State = store.StateNeedsYou
	b.Halt = "the plan needs a human: the scope widened"
	b.HaltNotifiedRound = b.Round
	b.RemoteHaltKind = store.HaltKindBroken
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{}
	rt := newRuntime(t)
	rt.Store = st
	rt.Remote = fr
	rt.Git = &fakeGit{branchExists: true}
	rt.Now = func() time.Time { return baseTime }

	got, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "api", Resume: true, MasterMindID: testMasterMindName, CWD: "/fake/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}

	if got.Halt != "" || !got.HaltAt.IsZero() || got.HaltNotifiedRound != 0 || got.RemoteHaltKind != "" {
		t.Errorf("resume kept halt %q kind %q key %d, want every field cleared",
			got.Halt, got.RemoteHaltKind, got.HaltNotifiedRound)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want %q after a resume", got.State, store.StateActive)
	}
}

// TestOlderBrokenHaltClearsOnARunningView pins the migration end to end: a
// record written before the kind existed names its break in the fallback text
// alone, and the next answering view has to clear it.
//
// Unnamed, the binding sits NEEDS YOU on a round the server says is running --
// until the round closes or a human re-sends it. The clear asks the kind, so
// without the kind on load it never happens.
func TestOlderBrokenHaltClearsOnARunningView(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.State = store.StateNeedsYou
	b.Halt = store.BrokenHaltText
	b.HaltNotifiedRound = b.Round
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	// Rewrite the record at the format before the kind existed, with no kind on
	// it: what an upgrade finds on disk.
	older := b
	older.Format = 15
	older.RemoteHaltKind = ""
	raw, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if _, err := d.RecordPut(db.Record{Owner: "", Name: b.Name, Round: b.Round, JSON: string(raw)}); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RemoteHaltKind != store.HaltKindBroken {
		t.Fatalf("RemoteHaltKind = %q, want %q recovered from the record", got.RemoteHaltKind, store.HaltKindBroken)
	}

	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile on the running view: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want %q: the round is running over there", got.State, store.StateActive)
	}
	if got.Halt != "" || got.RemoteHaltKind != "" {
		t.Errorf("halt %q kind %q, want both cleared by the running view", got.Halt, got.RemoteHaltKind)
	}
}

// observedMemberBinding is a remote binding that is a member of a chain running
// on a server, so its apply half observes the live round and stops short of the
// chain pull's own install and halt.
func observedMemberBinding(t *testing.T, st *store.Store, b store.Binding) store.Binding {
	t.Helper()
	row := db.ChainRow{
		ID: db.NewID(), Name: b.Name, Status: string(chain.StatusRunning),
		Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
		Plan:    1,
		Plans:   1,
		Builder: b.Name, Server: b.Builder.Server, CreatedAt: baseTime, UpdatedAt: baseTime,
	}
	if err := st.WithLock(func(tx *store.Tx) error { return tx.CreateChain(row, []store.Binding{b}) }); err != nil {
		t.Fatalf("CreateChain: %v", err)
	}
	got, err := st.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got
}

// TestObservedMemberClearsItsUnreachableHalt pins that the observe early return
// does not strand the unreachable halt it sits above. A member the server is
// running still gets halted by the unreachable arm, which does not consult the
// observe flag, so a needs_you or broken view has to clear it -- otherwise
// `wait` on the member keeps answering with the outage rather than with what
// the server says the round needs.
func TestObservedMemberClearsItsUnreachableHalt(t *testing.T) {
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

	got := unreachableHaltedBinding(t, rt, observedMemberBinding(t, st, b))
	if got.RemoteHaltKind != store.HaltKindUnreachable {
		t.Fatalf("RemoteHaltKind = %q, want %q as the fixture", got.RemoteHaltKind, store.HaltKindUnreachable)
	}

	const reason = "the reviewer needs a human: the patch touches the schema"
	fr.getBindingErr = nil
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: reason}

	got, err := reconcile(t, at(rt, 31*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile on the needs_you view: %v", err)
	}

	if got.RemoteHaltKind != "" {
		t.Fatalf("RemoteHaltKind = %q, want empty: the outage ended", got.RemoteHaltKind)
	}
	if strings.Contains(got.Halt, store.UnreachableHaltMarker) {
		t.Fatalf("Halt = %q, want the unreachable reason gone", got.Halt)
	}
}

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
