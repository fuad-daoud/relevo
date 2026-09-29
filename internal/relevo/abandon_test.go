package relevo

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// altSessionID is the session an opencode round announced in these tests.
const altSessionID = "ses-abandon-1"

// recordingExec records what a reaper ran and returns a scripted error.
type recordingExec struct {
	calls [][]string
	err   error
}

var _ usage.Exec = (*recordingExec)(nil)

func (f *recordingExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{bin}, args...))
	if f.err != nil {
		return nil, f.err
	}
	return nil, nil
}

// fakeDeleter records the sessions a reap deleted. onDelete runs inside each
// call, so a test can act while the reap is between its unlocked load and its
// locked save.
type fakeDeleter struct {
	calls    []store.AbandonedSession
	err      error
	onDelete func()
}

var _ SessionDeleter = (*fakeDeleter)(nil)

func (d *fakeDeleter) DeleteSession(ctx context.Context, s store.AbandonedSession) error {
	d.calls = append(d.calls, s)
	if d.onDelete != nil {
		d.onDelete()
	}
	return d.err
}

// opencodeSent binds webshop on the opencode candidate and sends one round
// through fr, then records the session the round announced. Only opencode's
// sessions are abandoned, so every call-site test uses this builder.
func opencodeSent(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.StreamSessionID = altSessionID
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return rt, b
}

// opencodeIdle is opencodeSent without the Send: no round is open, so a bound
// process is a stray.
func opencodeIdle(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

func TestSessionReaperRunsOpencodeDelete(t *testing.T) {
	t.Parallel()

	ex := &recordingExec{}
	if err := NewSessionReaper(ex).DeleteSession(context.Background(), store.AbandonedSession{Kind: "opencode", ID: "ses-1"}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	want := []string{"opencode", "session", "delete", "--standalone", "ses-1"}
	if len(ex.calls) != 1 || !reflect.DeepEqual(ex.calls[0], want) {
		t.Errorf("exec calls = %v, want %v", ex.calls, want)
	}
}

func TestSessionReaperTreatsSessionNotFoundAsSuccess(t *testing.T) {
	t.Parallel()

	ex := &recordingExec{err: errors.New("Session not found: ses-1")}
	if err := NewSessionReaper(ex).DeleteSession(context.Background(), store.AbandonedSession{Kind: "opencode", ID: "ses-1"}); err != nil {
		t.Errorf("DeleteSession = %v, want nil for a session that is already gone", err)
	}
}

func TestSessionReaperReturnsOtherErrors(t *testing.T) {
	t.Parallel()

	ex := &recordingExec{err: errors.New("database is locked")}
	err := NewSessionReaper(ex).DeleteSession(context.Background(), store.AbandonedSession{Kind: "opencode", ID: "ses-1"})
	if err == nil || !errors.Is(err, ex.err) {
		t.Errorf("DeleteSession = %v, want the exec error", err)
	}
}

func TestSessionReaperRefusesAKindItCannotDelete(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"claude", "agy", "codex", "future"} {
		ex := &recordingExec{}
		if err := NewSessionReaper(ex).DeleteSession(context.Background(), store.AbandonedSession{Kind: kind, ID: "s"}); err == nil {
			t.Errorf("DeleteSession(%q) = nil, want an error", kind)
		}
		if len(ex.calls) != 0 {
			t.Errorf("DeleteSession(%q) ran %v, want no exec", kind, ex.calls)
		}
	}
}

func TestAbandonSessionRecordsOpencodeAndIgnoresClaude(t *testing.T) {
	t.Parallel()

	b := store.Binding{Builder: store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless, StreamSessionID: altSessionID}}
	got := abandonSession(b)
	if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].Kind != "opencode" || got.AbandonedSessions[0].ID != altSessionID {
		t.Fatalf("AbandonedSessions = %+v, want the opencode session", got.AbandonedSessions)
	}
	if got.Builder.StreamSessionID != "" {
		t.Errorf("StreamSessionID = %q, want it cleared", got.Builder.StreamSessionID)
	}

	other := store.Binding{Builder: store.Endpoint{Kind: "claude", Mode: store.ModeHeadless, StreamSessionID: altSessionID}}
	got = abandonSession(other)
	if len(got.AbandonedSessions) != 0 {
		t.Errorf("claude AbandonedSessions = %+v, want none", got.AbandonedSessions)
	}
	if got.Builder.StreamSessionID != "" {
		t.Errorf("claude StreamSessionID = %q, want it cleared", got.Builder.StreamSessionID)
	}
}

func TestAbandonSessionDedupsAndRefusesAnEmptyID(t *testing.T) {
	t.Parallel()

	b := store.Binding{Builder: store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless, StreamSessionID: altSessionID}}
	b = abandonSession(b)
	// A second process announced the same session: it must not be recorded twice.
	b.Builder.StreamSessionID = altSessionID
	b = abandonSession(b)
	if len(b.AbandonedSessions) != 1 {
		t.Errorf("AbandonedSessions = %+v, want one entry", b.AbandonedSessions)
	}

	empty := store.Binding{Builder: store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless}}
	empty = abandonSession(empty)
	if len(empty.AbandonedSessions) != 0 {
		t.Errorf("empty id AbandonedSessions = %+v, want none", empty.AbandonedSessions)
	}

	pane := store.Binding{Builder: store.Endpoint{Kind: "opencode", StreamSessionID: altSessionID}}
	got := abandonSession(pane)
	if len(got.AbandonedSessions) != 0 || got.Builder.StreamSessionID != altSessionID {
		t.Errorf("non-headless = %+v / %q, want it untouched", got.AbandonedSessions, got.Builder.StreamSessionID)
	}
}

func TestReapableWaitsForAnUnannouncedProcess(t *testing.T) {
	t.Parallel()

	live := store.Binding{
		Builder:           store.Endpoint{Mode: store.ModeHeadless, PID: 7},
		AbandonedSessions: []store.AbandonedSession{{Kind: "opencode", ID: altSessionID}},
	}
	if reapable(live) {
		t.Error("reapable with a live process and no announced session = true, want false")
	}
	live.Builder.StreamSessionID = "ses-new"
	if !reapable(live) {
		t.Error("reapable after the session was announced = false, want true")
	}
	if reapable(store.Binding{Builder: store.Endpoint{Mode: store.ModeHeadless}}) {
		t.Error("reapable with no abandoned session = true, want false")
	}
}

func TestReapAbandonedRemovesDeletedAndCountsFailures(t *testing.T) {
	t.Parallel()

	t.Run("deleted", func(t *testing.T) {
		rt, b := opencodeSent(t, newFakeRunner())
		b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: altSessionID}}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		d := &fakeDeleter{}
		rt.SessionReaper = d

		reapAbandoned(context.Background(), rt, "webshop")

		if len(d.calls) != 1 || d.calls[0].ID != altSessionID {
			t.Fatalf("deleter calls = %+v, want the abandoned session", d.calls)
		}
		got, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.AbandonedSessions) != 0 {
			t.Errorf("AbandonedSessions = %+v, want them emptied", got.AbandonedSessions)
		}
	})

	t.Run("failure counts and drops at the limit", func(t *testing.T) {
		rt, b := opencodeSent(t, newFakeRunner())
		b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: altSessionID}}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		rt.SessionReaper = &fakeDeleter{err: errors.New("database is locked")}

		for i := 1; i <= reapMaxAttempts; i++ {
			reapAbandoned(context.Background(), rt, "webshop")
			got, err := rt.Store.Load("webshop")
			if err != nil {
				t.Fatal(err)
			}
			if i < reapMaxAttempts {
				if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].Attempts != i {
					t.Fatalf("attempt %d: AbandonedSessions = %+v, want one entry with %d attempts", i, got.AbandonedSessions, i)
				}
				continue
			}
			if len(got.AbandonedSessions) != 0 {
				t.Fatalf("attempt %d: AbandonedSessions = %+v, want the entry dropped", i, got.AbandonedSessions)
			}
		}
	})
}

func TestReapAbandonedKeepsAConcurrentlyAddedEntry(t *testing.T) {
	t.Parallel()

	rt, b := opencodeSent(t, newFakeRunner())
	b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: altSessionID}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	d := &fakeDeleter{}
	d.onDelete = func() {
		cur, err := rt.Store.Load("webshop")
		if err != nil {
			return
		}
		cur.AbandonedSessions = append(cur.AbandonedSessions, store.AbandonedSession{Kind: "opencode", ID: "ses-concurrent"})
		_ = rt.Store.Save(cur)
	}
	rt.SessionReaper = d

	reapAbandoned(context.Background(), rt, "webshop")

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].ID != "ses-concurrent" {
		t.Errorf("AbandonedSessions = %+v, want only the concurrently added entry", got.AbandonedSessions)
	}
}

func TestReapAbandonedSkipsWithoutAReaper(t *testing.T) {
	t.Parallel()

	rt, b := opencodeSent(t, newFakeRunner())
	b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: altSessionID}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	// rt.SessionReaper stays nil.

	reapAbandoned(context.Background(), rt, "webshop")

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AbandonedSessions) != 1 {
		t.Errorf("AbandonedSessions = %+v, want the entry kept", got.AbandonedSessions)
	}
}

// TestReapAbandonedHoldsNoLockDuringDelete pins that a harness delete runs
// free of the state lock: the fake deleter takes the lock itself, and the reap
// would deadlock if it held it.
func TestReapAbandonedHoldsNoLockDuringDelete(t *testing.T) {
	t.Parallel()

	rt, b := opencodeSent(t, newFakeRunner())
	b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: altSessionID}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	took := make(chan bool, 1)
	d := &fakeDeleter{}
	d.onDelete = func() {
		err := rt.Store.WithLock(func(tx *store.Tx) error { return nil })
		took <- err == nil
	}
	rt.SessionReaper = d

	done := make(chan struct{})
	go func() {
		reapAbandoned(context.Background(), rt, "webshop")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reapAbandoned deadlocked: the state lock was held during the delete")
	}
	if !<-took {
		t.Error("the deleter could not take the state lock: the delete ran under it")
	}
}

func TestLostToRestartResumeAbandonsTheOldSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := opencodeSent(t, fr)
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0) // the daemon started after the builder
	rt.Watched = NewWatched()                           // and saw nothing: this builder is lost
	fr.script(b.Builder.PID, false)                     // exited; no exit() set: code unknown
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the resume)", len(fr.specs))
	}
	if !containsArg(fr.specs[1].Argv, "--session", altSessionID) {
		t.Fatalf("resume argv = %v, want --session %s", fr.specs[1].Argv, altSessionID)
	}
	if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].Kind != "opencode" || got.AbandonedSessions[0].ID != altSessionID {
		t.Fatalf("AbandonedSessions = %+v, want the old session", got.AbandonedSessions)
	}
	if got.Builder.StreamSessionID != "" {
		t.Errorf("StreamSessionID = %q, want the new process's empty id", got.Builder.StreamSessionID)
	}
	if reapable(got) {
		t.Error("reapable after the resume = true, want false: the new process has not announced its session")
	}
}

func TestLostToRestartRequeueAbandonsTheSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := opencodeSent(t, fr)
	b.Owner = "owner1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)
	fr.script(b.Builder.PID, false)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1 (no relaunch)", len(fr.specs))
	}
	if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].ID != altSessionID {
		t.Fatalf("AbandonedSessions = %+v, want the re-queued round's session", got.AbandonedSessions)
	}
	if got.Builder.StreamSessionID != "" {
		t.Errorf("StreamSessionID = %q, want it cleared", got.Builder.StreamSessionID)
	}
}

func TestStrayKillAbandonsTheSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := opencodeIdle(t, fr) // no round open
	b.Builder.PID = 999
	b.Builder.StartedAt = 1_700_000_000
	b.Builder.LogPath = rt.Store.BuilderLogPath("webshop", 1)
	b.Builder.StreamSessionID = altSessionID
	fr.script(999, true)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != 999 {
		t.Fatalf("kills = %+v, want the stray 999", fr.kills)
	}
	if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].ID != altSessionID {
		t.Fatalf("AbandonedSessions = %+v, want the stray's session", got.AbandonedSessions)
	}
	if got.Builder.PID != 0 || got.Builder.StreamSessionID != "" {
		t.Errorf("process fields must clear: %+v", got.Builder)
	}
}

// switchableOpencode is sentSwitchable with opencode as the outgoing builder,
// so a gate on its token forces the switch the call site must handle.
func switchableOpencode(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", testOpencodeRef, testClaudeRef, "agy/other/m")
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.StreamSessionID = altSessionID
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return rt, b
}

func TestGatedSwitchAbandonsTheOldSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := switchableOpencode(t, fr)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testOpencodeRef, time.Time{}, "rate-limited"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// The gate covers opencode's whole provider, so the walk lands on the one
	// candidate on the other provider.
	if got.BuilderCandidate != "agy/other/m" {
		t.Fatalf("BuilderCandidate = %q, want agy/other/m", got.BuilderCandidate)
	}
	if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].Kind != "opencode" || got.AbandonedSessions[0].ID != altSessionID {
		t.Errorf("AbandonedSessions = %+v, want the switched-away session", got.AbandonedSessions)
	}
}

func TestSwitchLimitHaltAbandonsTheSessionWithoutAProcess(t *testing.T) {
	t.Parallel()

	rt, b := opencodeSent(t, newFakeRunner())
	b.Builder.PID = 0
	b.Builder.StartedAt = 0
	b.RoundSwitches = rt.Policy.SwitchLimit()
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = switchBuilder(context.Background(), rt, tx, b, "gate", false, false)
		return err
	})
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if out.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", out.State)
	}
	if len(out.AbandonedSessions) != 1 || out.AbandonedSessions[0].ID != altSessionID {
		t.Errorf("AbandonedSessions = %+v, want the session with no live process", out.AbandonedSessions)
	}
}

func TestStopAbandonsTheSessionAndReapsIt(t *testing.T) {
	t.Parallel()

	t.Run("records without a reaper", func(t *testing.T) {
		fr := newFakeRunner()
		rt, _ := opencodeSent(t, fr)
		// rt.SessionReaper stays nil, so the entry survives for inspection.
		if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		got, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.AbandonedSessions) != 1 || got.AbandonedSessions[0].ID != altSessionID {
			t.Fatalf("AbandonedSessions = %+v, want the stopped session", got.AbandonedSessions)
		}

		// The stopped round's report entry must name no session: the one it
		// would name is about to be deleted.
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		reports := 0
		for _, e := range entries {
			if e.Kind != store.KindReport {
				continue
			}
			reports++
			if e.BuilderSession != nil {
				t.Errorf("report BuilderSession = %+v, want none", e.BuilderSession)
			}
		}
		if reports == 0 {
			t.Fatal("no report entry: the stop must close the round")
		}
	})

	t.Run("deletes after the lock", func(t *testing.T) {
		fr := newFakeRunner()
		rt, _ := opencodeSent(t, fr)
		took := make(chan bool, 1)
		d := &fakeDeleter{}
		d.onDelete = func() {
			err := rt.Store.WithLock(func(tx *store.Tx) error { return nil })
			took <- err == nil
		}
		rt.SessionReaper = d

		done := make(chan error, 1)
		go func() {
			_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Stop: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Stop deadlocked: the state lock was held during the delete")
		}
		if !<-took {
			t.Error("the deleter could not take the state lock: the delete ran under it")
		}
		if len(d.calls) != 1 || d.calls[0].ID != altSessionID {
			t.Errorf("deleter calls = %+v, want the stopped session", d.calls)
		}
	})
}

func TestDoneAbandonsAndReapsTheSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := opencodeSent(t, fr)
	d := &fakeDeleter{}
	rt.SessionReaper = d

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if len(d.calls) != 1 || d.calls[0].ID != altSessionID {
		t.Errorf("deleter calls = %+v, want the stopped builder's session", d.calls)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AbandonedSessions) != 0 {
		t.Errorf("AbandonedSessions = %+v, want them reaped", got.AbandonedSessions)
	}
}

func TestDoneBetweenRoundsRecordsNothing(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := opencodeSent(t, fr)
	b.Builder.PID = 0
	b.Builder.StartedAt = 0
	b.Builder.StreamSessionID = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	d := &fakeDeleter{}
	rt.SessionReaper = d

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if len(d.calls) != 0 {
		t.Errorf("deleter calls = %+v, want none for a binding between rounds", d.calls)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AbandonedSessions) != 0 {
		t.Errorf("AbandonedSessions = %+v, want none", got.AbandonedSessions)
	}
}

func TestUnbindDeletesTheCurrentAndEarlierSessions(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := opencodeSent(t, fr)
	b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: "ses-earlier"}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	d := &fakeDeleter{}
	rt.SessionReaper = d

	if _, err := Unbind(context.Background(), rt, "webshop", false); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	var ids []string
	for _, s := range d.calls {
		ids = append(ids, s.ID)
	}
	if len(ids) != 2 || ids[0] != "ses-earlier" || ids[1] != altSessionID {
		t.Errorf("deleted sessions = %v, want [ses-earlier %s]", ids, altSessionID)
	}
}

func TestUnbindDeleterErrorIsNotFatal(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := opencodeSent(t, fr)
	b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: "ses-earlier"}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	rt.SessionReaper = &fakeDeleter{err: errors.New("database is locked")}

	res, err := Unbind(context.Background(), rt, "webshop", false)
	if err != nil {
		t.Fatalf("Unbind must still succeed: %v", err)
	}
	if res.ProcessStopped == 0 {
		t.Errorf("result = %+v, want the builder process reported stopped", res)
	}
	if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding still loads: %v", err)
	}
}

func TestTickReapsAbandonedSessions(t *testing.T) {
	t.Parallel()

	rt, b := opencodeSent(t, newFakeRunner())
	b.Builder.PID = 0
	b.Builder.StartedAt = 0
	b.Builder.StreamSessionID = "" // no live process, so nothing blocks the reap
	b.AbandonedSessions = []store.AbandonedSession{{Kind: "opencode", ID: altSessionID}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	d := &fakeDeleter{}
	rt.SessionReaper = d

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(d.calls) != 1 || d.calls[0].ID != altSessionID {
		t.Errorf("deleter calls = %+v, want the abandoned session", d.calls)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AbandonedSessions) != 0 {
		t.Errorf("AbandonedSessions = %+v, want the emptied list saved", got.AbandonedSessions)
	}
}
