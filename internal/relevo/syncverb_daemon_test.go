package relevo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests stay in this package because they need the daemon: they drive
// verbs through the daemon's own guard and queue, which live here and which
// the verb package must not import. They build their runners through the
// staying fixtures in syncverb_fixture_test.go and the exported verb API.

// TestSyncVerbSerializesWithTheDaemonGuard pins that a verb and a daemon trigger
// cannot interleave.
//
// Both record the same markers and both hold the same local file, so two of them
// at once would write each other's outcomes and leave a machine whose marker
// describes an attempt that never finished. The guard is the daemon's existing
// one: a verb waits its turn rather than being dropped, because somebody asked
// for it explicitly.
func TestSyncVerbSerializesWithTheDaemonGuard(t *testing.T) {
	f := newVerbFixture(t)
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Second)

	var mu sync.Mutex
	var concurrent int
	var maxConcurrent int
	// The client every attempt drives records its own entry and exit, so two
	// attempts at once are observable rather than inferred. The fake sleeps a
	// little inside each call so an unguarded runner really does overlap.
	probed := &overlapProbe{}
	probed.onEnter = func() {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()
	}
	probed.onHold = func() {
		// Held inside the entered window, so two attempts really are in flight
		// together if the guard lets them through. Sleeping after the decrement
		// would measure nothing.
		time.Sleep(5 * time.Millisecond)
	}
	probed.onExit = func() {
		mu.Lock()
		concurrent--
		mu.Unlock()
	}

	f.runner.Serialize = d.WaitSyncSlot
	f.runner.Runner.Client = probed

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
		}()
	}
	// The daemon's own triggers run in the same window, which is the case the
	// guard exists for.
	for range 4 {
		d.queueSync(context.Background())
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent > 1 {
		t.Errorf("%d syncs ran at once, want at most one: the daemon's guard is not serializing verbs", maxConcurrent)
	}
}

// overlapProbe is a log transport whose append reports entry and exit, so a
// test can see whether two attempts ever overlapped.
type overlapProbe struct {
	recordTransport
	onEnter func()
	onHold  func()
	onExit  func()
}

func (p *overlapProbe) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	p.onEnter()
	p.onHold()
	p.onExit()
	return p.recordTransport.Append(entries)
}

// hangingJoin is a sync log whose first pull hangs until it is cancelled, so a
// test can hold an enable's join open and watch a disable reach it. Its pull
// accounts through a real breaker the way the supervisor does, and a cancel
// releases it as a deliberate stop: the shape the pipe gives a hung pull.
type hangingJoin struct {
	breaker *relevosync.Breaker
	entered chan struct{}
	cancel  chan struct{}

	once       sync.Once
	cancelOnce sync.Once
	mu         sync.Mutex
	cancelled  bool
}

// errJoinStopped is what the fake's hung pull returns once it is cancelled.
var errJoinStopped = errors.New("test: the join's pull was stopped")

func newHangingJoin(b *relevosync.Breaker) *hangingJoin {
	return &hangingJoin{
		breaker: b,
		entered: make(chan struct{}),
		cancel:  make(chan struct{}),
	}
}

func (j *hangingJoin) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	return entries, nil
}
func (j *hangingJoin) Head(string) ([]synclog.HeadRow, error) { return nil, nil }
func (j *hangingJoin) Stats() (synclog.Stats, error)          { return synclog.Stats{}, nil }

func (j *hangingJoin) PublishMarks(map[string]int, time.Time) error { return nil }

func (j *hangingJoin) ReadMarks() (synclog.MarkState, error) { return synclog.MarkState{}, nil }

func (j *hangingJoin) Pull(map[string]int) ([]synclog.Entry, error) {
	if err := j.breaker.Begin("pull"); err != nil {
		return nil, err
	}
	j.once.Do(func() { close(j.entered) })
	<-j.cancel
	_ = j.breaker.End()
	return nil, errJoinStopped
}

func (j *hangingJoin) Cancel() {
	j.mu.Lock()
	j.cancelled = true
	j.mu.Unlock()
	j.cancelOnce.Do(func() { close(j.cancel) })
}

func (j *hangingJoin) wasCancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancelled
}

var _ synclog.LogTransport = (*hangingJoin)(nil)

// TestDisableStopsAnInFlightDaemonJoin pins the disable-during-a-join contract
// at the daemon's own guard: a disable issued while an enable's join runs in
// the daemon stops the join's worker, returns promptly rather than queuing
// behind the join's import bound, leaves the machine off, clears the join
// marker, and leaves the breaker's deaths at zero. That the disable reaches
// the join's own transport is pinned beside the runner in internal/sync/verb.
func TestDisableStopsAnInFlightDaemonJoin(t *testing.T) {
	f := newVerbFixture(t)
	if _, _, err := db.CompressHistoryOnce(f.shared, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Hour)
	f.runner.Serialize = d.WaitSyncSlot

	breaker := relevosync.NewBreaker(f.local)
	join := newHangingJoin(breaker)
	f.runner.Open = func(context.Context) (synclog.LogTransport, error) { return join, nil }

	ctx := context.Background()
	enableDone := make(chan struct{})
	go func() {
		defer close(enableDone)
		f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbEnable}, []byte(verbFixtureToken))
	}()

	select {
	case <-join.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the join did not reach its pull")
	}

	done := make(chan *wire.SyncResult, 1)
	go func() { done <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil) }()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("disable refused: %s", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the disable queued behind the join instead of stopping it")
	}
	<-enableDone

	if !join.wasCancelled() {
		t.Error("the join's worker was never cancelled")
	}
	if on, err := relevosync.Enabled(f.local); err != nil || on {
		t.Errorf("Enabled = %v, %v; want the released join to leave the machine off", on, err)
	}
	if _, ok, err := relevosync.ReadJoin(f.local); err != nil || ok {
		t.Errorf("join marker = (ok %v, err %v); want it cleared by the disable", ok, err)
	}
	if deaths, err := breaker.Deaths(); err != nil || deaths != 0 {
		t.Errorf("breaker deaths = %d, %v; want the deliberate stop uncounted", deaths, err)
	}
}

// hungSteadyPull builds a fixture whose runner drives a worker that hangs on its
// first pull, with the daemon's guard installed and the machine marked on, so a
// test can hold the sync slot with a steady pull rather than a join.
func hungSteadyPull(t *testing.T) (*verbFixture, *hangingJoin, *relevosync.Breaker) {
	t.Helper()
	f := newVerbFixture(t)
	if err := relevosync.MarkEnabled(f.local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Hour)
	f.runner.Serialize = d.WaitSyncSlot

	breaker := relevosync.NewBreaker(f.local)
	hung := newHangingJoin(breaker)
	f.runner.Runner.Client = hung
	return f, hung, breaker
}

// waitForHungPull blocks until the fixture's worker is inside its hung pull, so
// a test can be sure the sync slot is held before it issues its verb.
func waitForHungPull(t *testing.T, hung *hangingJoin) {
	t.Helper()
	select {
	case <-hung.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the pull did not reach its worker")
	}
}

// TestDisableStopsAHungSteadyPullAtTheDaemon pins the disable-during-a-hung-
// steady-pull contract at the daemon's own guard: a pull against a worker that
// has stopped answering holds the sync slot, and a disable issued then releases
// that call before it queues, so the disable runs in seconds rather than waiting
// the data-call bound out. The released call settles as a deliberate stop, so
// the breaker counts no death, and the machine is left off.
func TestDisableStopsAHungSteadyPullAtTheDaemon(t *testing.T) {
	f, hung, breaker := hungSteadyPull(t)
	ctx := context.Background()

	pullDone := make(chan *wire.SyncResult, 1)
	go func() { pullDone <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil) }()
	waitForHungPull(t, hung)

	done := make(chan *wire.SyncResult, 1)
	go func() { done <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil) }()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("disable refused: %s", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the disable queued behind the hung pull instead of stopping it")
	}
	<-pullDone

	if !hung.wasCancelled() {
		t.Error("the hung pull's worker was never cancelled")
	}
	if deaths, err := breaker.Deaths(); err != nil || deaths != 0 {
		t.Errorf("breaker deaths = %d, %v; want the deliberate stop uncounted", deaths, err)
	}
	if on, err := relevosync.Enabled(f.local); err != nil || on {
		t.Errorf("Enabled = %v, %v; want the released pull to leave the machine off", on, err)
	}
	// The disable dropped the runner's worker, so the next call opens a fresh
	// one through the wiring's opener rather than driving the killed pipe.
	f.opens = 0
	if res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil); !res.OK {
		t.Fatalf("the pull after the disable refused: %s", res.Message)
	}
	if f.opens != 1 {
		t.Errorf("the next pull opened %d workers, want a fresh one", f.opens)
	}
}

// TestRetryStopsAHungSteadyPullAtTheDaemon pins the retry half of the same
// contract: a retry is sent precisely because a call has stopped answering, so
// it releases the hung call before it queues rather than waiting the data-call
// bound out for a slot the call it is clearing holds.
func TestRetryStopsAHungSteadyPullAtTheDaemon(t *testing.T) {
	f, hung, breaker := hungSteadyPull(t)
	ctx := context.Background()

	pullDone := make(chan *wire.SyncResult, 1)
	go func() { pullDone <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil) }()
	waitForHungPull(t, hung)

	done := make(chan *wire.SyncResult, 1)
	go func() { done <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbRetry}, nil) }()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("retry refused: %s", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retry queued behind the hung pull instead of stopping it")
	}
	<-pullDone

	if !hung.wasCancelled() {
		t.Error("the hung pull's worker was never cancelled")
	}
	if deaths, err := breaker.Deaths(); err != nil || deaths != 0 {
		t.Errorf("breaker deaths = %d, %v; want the deliberate stop uncounted", deaths, err)
	}
}

func (h *hangingJoin) PullRange(string, int, int) ([]synclog.Entry, error) { return nil, nil }

func (h *hangingJoin) CompactOwn(int) (synclog.CompactResult, error) {
	return synclog.CompactResult{}, nil
}

func (h *hangingJoin) KeyMeta(context.Context) (synclog.KeyMeta, error) {
	return synclog.KeyMeta{}, nil
}

func (h *hangingJoin) KeyInit(context.Context, synclog.KeyMeta) (synclog.KeyMeta, error) {
	return synclog.KeyMeta{}, nil
}

func (h *hangingJoin) KeyRekey(context.Context, synclog.KeyRekeySpec) (synclog.KeyMeta, error) {
	return synclog.KeyMeta{}, nil
}

func (h *hangingJoin) PublishEpochCut(context.Context, synclog.EpochCut) error { return nil }

func (h *hangingJoin) ClaimIdentity(context.Context, string, string, time.Time) (string, time.Time, error) {
	return "", time.Time{}, nil
}

// TestDaemonJoiningReportsNoJoinWhenUnbuilt pins the daemon's nil case: a
// daemon not yet built shares no runner's join, so it reports none.
func TestDaemonJoiningReportsNoJoinWhenUnbuilt(t *testing.T) {
	if (*Daemon)(nil).Joining() {
		t.Error("a daemon not yet built reports a join")
	}
}
