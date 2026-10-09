package relevo

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The tests here drive the daemon's triggers against a log that counts pulls:
// every attempt pulls, so the pull count over a measured per-attempt baseline
// is the number of attempts the triggers caused.

// gateLog counts pulls and can hold the next one, which is how a test lands a
// trigger in the middle of an attempt.
type gateLog struct {
	synclog.LogTransport
	pulls atomic.Int64

	mu      sync.Mutex
	failure error
	entered chan struct{}
	release chan struct{}
}

func (g *gateLog) Pull(marks map[string]int) ([]synclog.Entry, error) {
	g.pulls.Add(1)
	g.mu.Lock()
	entered, release, failure := g.entered, g.release, g.failure
	g.entered, g.release = nil, nil
	g.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		<-release
	}
	if failure != nil {
		return nil, failure
	}
	return g.LogTransport.Pull(marks)
}

// hold makes the next pull wait until the returned release is called, and
// signals entered when it is waiting.
func (g *gateLog) hold() (entered <-chan struct{}, release func()) {
	in, out := make(chan struct{}, 1), make(chan struct{})
	g.mu.Lock()
	g.entered, g.release = in, out
	g.mu.Unlock()
	return in, func() { close(out) }
}

type liveFixture struct {
	d     *Daemon
	mdb   *db.DB
	local relevosync.Local
	log   *gateLog
	far   *synclog.MemTransport
	known int
	nanos atomic.Int64
	per   int64
}

// newLiveFixture is a daemon over a real machine database with sync turned on
// and a clock the test moves.
func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	st := store.New(t.TempDir())
	mdb, err := st.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, baseTime); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	f := &liveFixture{mdb: mdb, local: local, far: synclog.NewMemTransport("m2")}
	_, f.known = mdb.SchemaVersions()
	f.log = &gateLog{LogTransport: f.far.OnLog(mdb.Origin())}
	rt := Runtime{Store: st, Sync: &relevosync.Runner{Client: f.log, Local: local}}
	f.d = NewDaemon(rt, time.Second)
	f.setNow(baseTime)
	f.d.syncNow = func() time.Time { return time.Unix(0, f.nanos.Load()) }
	return f
}

func (f *liveFixture) setNow(at time.Time) { f.nanos.Store(at.UnixNano()) }

// baseline runs one attempt and learns how many pulls an attempt makes.
func (f *liveFixture) baseline(t *testing.T) {
	t.Helper()
	f.d.queueSync(context.Background())
	waitSyncIdle(t, f.d)
	f.per = f.log.pulls.Load()
	if f.per == 0 {
		t.Fatal("an attempt made no pull, so attempts cannot be counted")
	}
}

func (f *liveFixture) attempts() int64 { return f.log.pulls.Load() / f.per }

func TestSyncTriggerMidAttemptRerunsExactlyOnce(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	base := f.attempts()

	entered, release := f.log.hold()
	f.d.queueSync(context.Background())
	<-entered
	// Three writes land while the attempt is in flight: one rerun, not three.
	for range 3 {
		f.d.queueSync(context.Background())
	}
	release()
	waitSyncIdle(t, f.d)

	if got := f.attempts() - base; got != 2 {
		t.Errorf("a trigger mid-attempt caused %d attempts in total, want the attempt and exactly one rerun", got)
	}
}

func TestSyncIdleTriggerStartsOneAttempt(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	base := f.attempts()

	f.d.queueSync(context.Background())
	waitSyncIdle(t, f.d)

	if got := f.attempts() - base; got != 1 {
		t.Errorf("an idle trigger caused %d attempts, want 1", got)
	}
}

func TestSyncExportDebounce(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	ctx := context.Background()

	f.setNow(baseTime)
	f.d.idleSync(ctx)
	waitSyncIdle(t, f.d)
	opened := f.attempts()

	seedOutboxWrite(t, f.mdb, "debounce")
	f.setNow(baseTime.Add(exportDebounce - time.Second))
	f.d.idleSync(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != opened {
		t.Errorf("an export one second inside the debounce started an attempt (%d -> %d)", opened, got)
	}

	f.setNow(baseTime.Add(exportDebounce))
	f.d.idleSync(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != opened+1 {
		t.Errorf("attempts at the debounce boundary = %d, want %d", got, opened+1)
	}

	f.setNow(baseTime.Add(3 * exportDebounce))
	f.d.idleSync(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != opened+1 {
		t.Errorf("an empty outbox started an attempt (%d -> %d)", opened+1, got)
	}
}

func TestSyncPullWindowIsHotAfterAppliedRowsAndColdOtherwise(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		hot  bool
		at   time.Duration
		want int64
	}{
		{"hot window opens after hotWindow", true, hotWindow, 1},
		{"hot window is closed inside hotWindow", true, hotWindow - time.Second, 0},
		{"cold machine is closed after hotWindow", false, hotWindow, 0},
		{"cold window opens after coldWindow", false, coldWindow, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newLiveFixture(t)
			f.baseline(t)
			ctx := context.Background()

			f.setNow(baseTime)
			f.d.idleSync(ctx)
			waitSyncIdle(t, f.d)
			opened := f.attempts()
			if tc.hot {
				f.d.syncMu.Lock()
				f.d.live.appliedAt = baseTime
				f.d.syncMu.Unlock()
			}

			f.setNow(baseTime.Add(tc.at))
			f.d.idleSync(ctx)
			waitSyncIdle(t, f.d)
			if got := f.attempts() - opened; got != tc.want {
				t.Errorf("attempts after %v = %d, want %d", tc.at, got, tc.want)
			}
		})
	}
}

// TestSyncHotWindowCoolsAfterHotSince pins that the hot window ends: a machine
// that applied rows once must not pull every hotWindow for the rest of its life.
func TestSyncHotWindowCoolsAfterHotSince(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.d.syncMu.Lock()
	defer f.d.syncMu.Unlock()
	f.d.live.appliedAt = baseTime
	for _, tc := range []struct {
		after time.Duration
		want  time.Duration
	}{
		{hotSince - time.Second, hotWindow},
		{hotSince, coldWindow},
		{24 * time.Hour, coldWindow},
	} {
		if got := f.d.pullWindowLocked(baseTime.Add(tc.after)); got != tc.want {
			t.Errorf("window %v after rows applied = %v, want %v", tc.after, got, tc.want)
		}
	}
}

func TestSyncAppliedRowsTurnTheWindowHot(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	seedFar(t, f.far, "m2", "far-1", f.known)
	f.baseline(t)

	f.d.syncMu.Lock()
	hot := f.d.pullWindowLocked(f.d.syncClock()())
	f.d.syncMu.Unlock()
	if hot != hotWindow {
		t.Errorf("window after an attempt applied rows = %v, want %v", hot, hotWindow)
	}
}

func TestSyncFreshenThrottle(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	ctx := context.Background()

	f.setNow(baseTime)
	f.d.queueSync(ctx)
	waitSyncIdle(t, f.d)
	started := f.attempts()

	f.setNow(baseTime.Add(exportDebounce))
	f.d.Freshen(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != started {
		t.Errorf("a freshen within the throttle started an attempt (%d -> %d)", started, got)
	}

	f.setNow(baseTime.Add(exportDebounce + time.Second))
	f.d.Freshen(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != started+1 {
		t.Errorf("attempts after a freshen past the throttle = %d, want %d", got, started+1)
	}
}

func TestSyncLatchedBreakerRefusesATrigger(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	base := f.attempts()
	if err := f.local.KVPut(relevosync.KeyAttention, []byte(`{"message":"latched for the test"}`)); err != nil {
		t.Fatalf("latch: %v", err)
	}

	f.d.queueSync(context.Background())
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != base {
		t.Errorf("a latched machine started an attempt (%d -> %d)", base, got)
	}
}

func TestSyncTriggerDuringAJoinWaitsForNothingAndDoesNotInterleave(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	base := f.attempts()

	inJoin, endJoin := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.d.WaitSyncSlot(func() {
			close(inJoin)
			<-endJoin
		})
	}()
	<-inJoin
	f.d.queueSync(context.Background())
	if got := f.attempts(); got != base {
		t.Errorf("a trigger during a join drove the pipeline (%d -> %d)", base, got)
	}
	close(endJoin)
	<-done
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != base {
		t.Errorf("a trigger dropped by a join ran after it (%d -> %d)", base, got)
	}
}

func TestSyncCleanAttemptIsRecorded(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	state, err := relevosync.ReadState(f.local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.Attempt.Start.IsZero() || state.Attempt.Failed() {
		t.Errorf("a clean attempt recorded %+v", state.Attempt)
	}
}

func TestSyncFailedAttemptIsRecordedAndReadsBehind(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	f.log.mu.Lock()
	f.log.failure = errors.New("the remote said no")
	f.log.mu.Unlock()

	f.d.queueSync(context.Background())
	waitSyncIdle(t, f.d)

	state, err := relevosync.ReadState(f.local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !state.Attempt.Failed() || !strings.Contains(state.Attempt.Error, "the remote said no") {
		t.Errorf("a failed attempt recorded %+v", state.Attempt)
	}
	if got := relevosync.Token(state); got != relevosync.TokenBehind {
		t.Errorf("token after a failed attempt = %q, want %q", got, relevosync.TokenBehind)
	}
}

// TestSyncUnopenableRemoteIsRecordedAndReadsBehind pins that an attempt which
// cannot even open the stored remote is recorded as failed: a daemon whose
// every tick stops there must not read as healthy.
func TestSyncUnopenableRemoteIsRecordedAndReadsBehind(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.d.SetSyncVerbs(&VerbRunner{Local: f.local, Runner: f.d.rt.Sync})

	f.d.queueSync(context.Background())
	waitSyncIdle(t, f.d)

	state, err := relevosync.ReadState(f.local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !state.Attempt.Failed() || state.Attempt.Start.IsZero() {
		t.Errorf("an attempt that could not open its remote recorded %+v", state.Attempt)
	}
	if got := relevosync.Token(state); got != relevosync.TokenBehind {
		t.Errorf("token after an unopenable remote = %q, want %q", got, relevosync.TokenBehind)
	}
}
