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
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/store/storetest"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
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
	t     *testing.T
}

// newLiveFixture is a daemon over a real machine database with sync turned on
// and a clock the test moves.
func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	st := storetest.New(t)
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
	f := &liveFixture{t: t, mdb: mdb, local: local, far: synclog.NewMemTransport("m2")}
	_, f.known = mdb.SchemaVersions()
	f.log = &gateLog{LogTransport: f.far.OnLog(mdb.Origin())}
	rt := Runtime{Store: st, Sync: &relevosync.Runner{Client: f.log, Local: local}}
	f.d = NewDaemon(rt, time.Second)
	f.setNow(baseTime)
	f.d.syncNow = func() time.Time { return time.Unix(0, f.nanos.Load()) }
	// Settled: a machine that has been on for longer than the warm period, so
	// the cold-window and debounce tests pin a long-running daemon.
	f.d.syncMu.Lock()
	f.d.live.OnSince = baseTime.Add(-verb.HotSince)
	f.d.syncMu.Unlock()
	return f
}

// newOffFixture is a daemon that has been running with sync off and is about
// to be enabled.
func newOffFixture(t *testing.T) *liveFixture {
	t.Helper()
	f := newLiveFixture(t)
	f.baseline(t)
	if err := relevosync.MarkEnabled(f.local, false, baseTime); err != nil {
		t.Fatalf("MarkEnabled off: %v", err)
	}
	f.d.syncMu.Lock()
	f.d.live.OnSince = time.Time{}
	f.d.syncLast = time.Time{}
	f.d.syncMu.Unlock()
	return f
}

// enableAt turns sync on for the fixture's daemon.
func (f *liveFixture) enableAt(t *testing.T, on bool, at time.Time) {
	t.Helper()
	if err := relevosync.MarkEnabled(f.local, on, at); err != nil {
		t.Fatalf("MarkEnabled %v: %v", on, err)
	}
}

// tick runs one idle tick at the given time and waits for its attempt.
func (f *liveFixture) tick(at time.Time) {
	f.setNow(at)
	f.d.idleSync(context.Background())
	waitSyncIdle(f.t, f.d)
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
	f.setNow(baseTime.Add(verb.ExportDebounce - time.Second))
	f.d.idleSync(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != opened {
		t.Errorf("an export one second inside the debounce started an attempt (%d -> %d)", opened, got)
	}

	f.setNow(baseTime.Add(verb.ExportDebounce))
	f.d.idleSync(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != opened+1 {
		t.Errorf("attempts at the debounce boundary = %d, want %d", got, opened+1)
	}

	// The export trigger alone: an idle tick this late would also open the cold
	// pull window, which is no smaller than the export gap.
	at := baseTime.Add(3 * verb.ExportDebounce)
	f.setNow(at)
	f.d.exportIfPending(ctx, at)
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
		{"hot window opens after verb.HotWindow", true, verb.HotWindow, 1},
		{"hot window is closed inside verb.HotWindow", true, verb.HotWindow - time.Second, 0},
		{"cold machine is closed after verb.HotWindow", false, verb.HotWindow, 0},
		{"cold window opens after verb.ColdWindow", false, verb.ColdWindow, 1},
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
				f.d.live.AppliedAt = baseTime
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
// that applied rows once must not pull every verb.HotWindow for the rest of its life.
func TestSyncHotWindowCoolsAfterHotSince(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.d.syncMu.Lock()
	defer f.d.syncMu.Unlock()
	f.d.live.AppliedAt = baseTime
	for _, tc := range []struct {
		after time.Duration
		want  time.Duration
	}{
		{verb.HotSince - time.Second, verb.HotWindow},
		{verb.HotSince, verb.ColdWindow},
		{24 * time.Hour, verb.ColdWindow},
	} {
		if got := f.d.pullWindowLocked(baseTime.Add(tc.after)); got != tc.want {
			t.Errorf("window %v after rows applied = %v, want %v", tc.after, got, tc.want)
		}
	}

	f.d.live.AppliedAt = time.Time{}
	f.d.live.OnSince = baseTime
	for _, tc := range []struct {
		after time.Duration
		want  time.Duration
	}{
		{verb.HotSince - time.Second, verb.HotWindow},
		{verb.HotSince, verb.ColdWindow},
	} {
		if got := f.d.pullWindowLocked(baseTime.Add(tc.after)); got != tc.want {
			t.Errorf("window %v after sync came on = %v, want %v", tc.after, got, tc.want)
		}
	}
}

// TestSyncEnableWhileRunningFollowsWithASecondAttempt pins that a daemon that
// finds sync turned on pulls again after verb.HotWindow, with nothing applied and
// nothing in the outbox to account for it.
func TestSyncEnableWhileRunningFollowsWithASecondAttempt(t *testing.T) {
	t.Parallel()
	f := newOffFixture(t)
	for _, d := range []time.Duration{0, 2 * time.Second, 4 * time.Second} {
		f.tick(baseTime.Add(d))
	}
	if got := f.attempts(); got != 1 {
		t.Fatalf("attempts while off = %d, want the baseline 1", got)
	}

	t0 := baseTime.Add(10 * time.Second)
	f.enableAt(t, true, t0)
	f.tick(t0)
	if got := f.attempts(); got != 2 {
		t.Fatalf("attempts at enable = %d, want 2", got)
	}
	f.tick(t0.Add(verb.HotWindow - time.Second))
	if got := f.attempts(); got != 2 {
		t.Fatalf("attempts inside verb.HotWindow = %d, want 2", got)
	}
	f.tick(t0.Add(verb.HotWindow))
	if got := f.attempts(); got != 3 {
		t.Errorf("attempts at verb.HotWindow = %d, want 3", got)
	}
	if f.d.outboxPending() {
		t.Error("outbox has entries, so the second attempt is not the warm period's")
	}
	f.d.syncMu.Lock()
	applied := f.d.live.AppliedAt
	f.d.syncMu.Unlock()
	if !applied.IsZero() {
		t.Errorf("appliedAt = %v, want zero", applied)
	}
}

// TestSyncWarmPeriodEndsAfterHotSince pins that a quiet machine returns to the
// cold cadence.
func TestSyncWarmPeriodEndsAfterHotSince(t *testing.T) {
	t.Parallel()
	f := newOffFixture(t)
	t0 := baseTime.Add(10 * time.Second)
	f.enableAt(t, true, t0)
	f.tick(t0)
	f.tick(t0.Add(verb.HotSince - time.Second))
	opened := f.attempts()
	f.tick(t0.Add(verb.HotSince + verb.HotWindow))
	if got := f.attempts() - opened; got != 0 {
		t.Errorf("attempts after the warm period = %d, want 0", got)
	}
}

// TestSyncReEnableWarmsAgain pins that turning sync off and on in one process
// opens the first window at once and warms the window again.
func TestSyncReEnableWarmsAgain(t *testing.T) {
	t.Parallel()
	f := newOffFixture(t)
	t0 := baseTime.Add(10 * time.Second)
	f.enableAt(t, true, t0)
	f.tick(t0)
	f.tick(t0.Add(verb.HotWindow))

	off := t0.Add(verb.HotWindow + time.Second)
	f.enableAt(t, false, off)
	f.tick(off)

	t1 := off.Add(time.Second)
	f.enableAt(t, true, t1)
	before := f.attempts()
	f.tick(t1)
	if got := f.attempts() - before; got != 1 {
		t.Fatalf("attempts at re-enable = %d, want 1", got)
	}
	f.tick(t1.Add(verb.HotWindow))
	if got := f.attempts() - before; got != 2 {
		t.Errorf("attempts at verb.HotWindow after re-enable = %d, want 2", got)
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
	if hot != verb.HotWindow {
		t.Errorf("window after an attempt applied rows = %v, want %v", hot, verb.HotWindow)
	}
}

// lastSyncStart is when the fixture daemon's last attempt began. An owed attempt
// fails before its pull against the fake log, which cannot take a signed send, so
// the start is what shows the gap let an attempt through.
func (f *liveFixture) lastSyncStart() time.Time {
	f.d.syncMu.Lock()
	defer f.d.syncMu.Unlock()
	return f.d.live.StartedAt
}

// clearDocSignals strikes the fixture database's pending doc signals, which a
// machine holding the key would otherwise count as something to send.
func clearDocSignals(t *testing.T, mdb *db.DB) {
	t.Helper()
	signals, err := mdb.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	for _, sig := range signals {
		if err := mdb.LocalOrSelf().Tx(func(tx *db.Tx) error { return tx.ClearDocSignals(sig.Doc) }); err != nil {
			t.Fatalf("ClearDocSignals %s: %v", sig.Doc, err)
		}
	}
	if left, err := mdb.DocSignals(); err != nil || len(left) > 0 {
		t.Fatalf("doc signals left after clearing: %d, %v", len(left), err)
	}
}

// TestSyncOwedHistoryKeepsTheShortGap pins that a join's owed history is not held
// to the long export gap: each attempt sends one slice, so the long gap would
// stretch the send twentyfold. The outbox is empty, so the owed history alone has
// to count as something to send.
func TestSyncOwedHistoryKeepsTheShortGap(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	f.tick(baseTime)
	opened := f.lastSyncStart()
	clearDocSignals(t, f.mdb)

	if err := relevosync.SetKey(f.local, make([]byte, 32), baseTime); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := relevosync.WriteJoin(f.local, baseTime); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	f.tick(baseTime.Add(verb.OwedDebounce - time.Second))
	if got := f.lastSyncStart(); !got.Equal(opened) {
		t.Fatalf("owed history started an attempt at %v inside the short gap", got)
	}

	at := baseTime.Add(verb.OwedDebounce)
	f.tick(at)
	if got := f.lastSyncStart(); !got.Equal(at) {
		t.Errorf("last attempt began %v, want an attempt with owed history at the short gap %v", got, at)
	}
	f.d.syncMu.Lock()
	exported := f.d.live.ExportedAt
	f.d.syncMu.Unlock()
	if !exported.Equal(at) {
		t.Errorf("the owed-history attempt at %v did not export (last export began %v)", at, exported)
	}
}

// TestSyncLockedJoinKeepsTheExportGap pins that a locked machine's join marker
// does not shorten the gap: it cannot sign its owed history, so an attempt every
// short gap would only pull.
func TestSyncLockedJoinKeepsTheExportGap(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	f.tick(baseTime)
	opened := f.lastSyncStart()

	seedOutboxWrite(t, f.mdb, "locked")
	if err := relevosync.WriteJoin(f.local, baseTime); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	f.tick(baseTime.Add(verb.OwedDebounce))
	if got := f.lastSyncStart(); !got.Equal(opened) {
		t.Errorf("a locked join's outbox write started an attempt at %v inside the export gap", got)
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

	f.setNow(baseTime.Add(verb.ExportDebounce))
	f.d.Freshen(ctx)
	waitSyncIdle(t, f.d)
	if got := f.attempts(); got != started {
		t.Errorf("a freshen within the throttle started an attempt (%d -> %d)", started, got)
	}

	f.setNow(baseTime.Add(verb.ExportDebounce + time.Second))
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

// TestSyncAttemptVerbStartsAnAttempt pins that the attempt verb a gate or a
// takeover sends hurries an attempt through the daemon's own wiring: the verb
// runs under the slot guard, which drops a trigger landing while it holds the
// slot, so an attempt verb run inside that guard would never start anything.
func TestSyncAttemptVerbStartsAnAttempt(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	ctx := context.Background()
	f.tick(baseTime)

	at := baseTime.Add(time.Minute)
	f.setNow(at)
	runner := &verb.Runner{Local: f.local, Runner: f.d.rt.Sync, Serialize: f.d.WaitSyncSlot}
	runner.Trigger = func() { f.d.SyncNow(ctx) }
	if res := runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbAttempt}, nil); !res.OK {
		t.Fatalf("attempt verb answered %+v", res)
	}
	waitSyncIdle(t, f.d)
	if got := f.lastSyncStart(); !got.Equal(at) {
		t.Errorf("last attempt began %v, want the attempt verb's %v inside the export gap", got, at)
	}
}

// outboxPendingNow reads whether the fixture's outbox still holds entries, which
// is what tells a pull-only attempt from one that exported.
func (f *liveFixture) outboxPendingNow() bool {
	f.t.Helper()
	has, err := f.mdb.HasOutboxEntries()
	if err != nil {
		f.t.Fatalf("HasOutboxEntries: %v", err)
	}
	return has
}

// warm marks the fixture daemon as having just applied rows, so its pull window
// is the hot one.
func (f *liveFixture) warm(at time.Time) {
	f.d.syncMu.Lock()
	f.d.live.AppliedAt = at
	f.d.syncMu.Unlock()
}

// TestSyncPullWindowInsideTheGapOnlyPulls pins that an attempt the hot pull
// window starts inside the export gap leaves the outbox alone: two busy machines
// keep each other's window hot, so an export on every pull would undo the gap.
func TestSyncPullWindowInsideTheGapOnlyPulls(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	f.tick(baseTime)
	if f.outboxPendingNow() {
		t.Fatal("the opening attempt left the outbox holding entries")
	}

	seedOutboxWrite(t, f.mdb, "pull-only")
	f.warm(baseTime)
	at := baseTime.Add(verb.HotWindow)
	f.tick(at)
	if got := f.lastSyncStart(); !got.Equal(at) {
		t.Fatalf("the hot window started no attempt at %v (last began %v)", at, got)
	}
	if !f.outboxPendingNow() {
		t.Errorf("an attempt inside the export gap exported the outbox")
	}

	f.tick(baseTime.Add(verb.ExportDebounce))
	if f.outboxPendingNow() {
		t.Errorf("the attempt at the export gap left the outbox holding entries")
	}
}

// TestSyncNowDuringAPullOnlyAttemptExports pins that a SyncNow landing while a
// pull-only attempt holds the slot is not absorbed by it: the rerun exports.
func TestSyncNowDuringAPullOnlyAttemptExports(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	f.baseline(t)
	ctx := context.Background()
	f.tick(baseTime)

	seedOutboxWrite(t, f.mdb, "now")
	f.warm(baseTime)
	f.setNow(baseTime.Add(verb.HotWindow))
	entered, release := f.log.hold()
	f.d.idleSync(ctx)
	<-entered
	f.d.SyncNow(ctx)
	release()
	waitSyncIdle(t, f.d)
	if f.outboxPendingNow() {
		t.Errorf("a SyncNow during a pull-only attempt left the outbox holding entries")
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
	f.d.SetSyncVerbs(&verb.Runner{Local: f.local, Runner: f.d.rt.Sync})

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

// TestSyncTickWithNoBindingStillPulls pins that the idle window is on the tick
// for a machine with no binding of its own: such a machine still has to pull
// what the other machines wrote, so the no-bindings return must not skip it.
func TestSyncTickWithNoBindingStillPulls(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t)
	if err := f.d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, f.d)
	if f.log.pulls.Load() == 0 {
		t.Error("a tick on a machine with no binding started no sync attempt")
	}
}
