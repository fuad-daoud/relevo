package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// tickUntil polls cond until it holds, failing the test past the bound. A
// fan-out lands on its own goroutine, so the test joins it by polling, never
// by sleeping a fixed span.
func tickUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stopFanoutSetup is answerSetup with a controllable clock: the head, a
// scripted TLS node, and one queued round placed as a sending forward. The
// tick and the retry read the same clock the test advances.
func stopFanoutSetup(t *testing.T, script *nodeScript, clock *time.Time) (*testEnv, burst.NodeRow, store.Forward) {
	t.Helper()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return *clock }
	})
	url, fp := startStubNode(t, script.serve())
	row := stubRow(t, "b1", url, fp)
	fwd := queueForward(t, env, "api", row, *clock)
	return env, row, fwd
}

// stampForward flips the named binding's forward to phase with the stop
// stamped, simulating the stamp a Stop left behind when its fan-out never
// ran: the shape the retry and the lost-node paths read.
func stampForward(t *testing.T, rt relevo.Runtime, name, phase string) store.Forward {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	have := burstStoredForward(b)
	if have == nil {
		t.Fatalf("%s has no forward to stamp", name)
	}
	have.Phase = phase
	have.StopRequested = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return *have
}

// hookJoin records fan-out completions for tickUntil to join.
type hookJoin struct {
	mu   sync.Mutex
	errs []error
}

func (j *hookJoin) hook(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.errs = append(j.errs, err)
}

func (j *hookJoin) fired() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.errs) > 0
}

func (j *hookJoin) first() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.errs) == 0 {
		return nil
	}
	return j.errs[0]
}

// count reports how many fan-out attempts have landed.
func (j *hookJoin) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.errs)
}

// allNil reports whether every landed attempt carried no error.
func (j *hookJoin) allNil() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.errs {
		if e != nil {
			return false
		}
	}
	return true
}

// TestFanoutStopHeadNeverBlocks pins the head side of the fan-out: Stop
// records the request and returns while the node's answer is still pending,
// and the detached send lands once the test lets the node answer.
func TestFanoutStopHeadNeverBlocks(t *testing.T) {
	clock := time.Now().UTC()
	release := make(chan struct{})
	script := &nodeScript{
		getStatus: 200, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		createStatus: 200,
		startStatus:  201, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
		stopStatus: 200, stopView: stubRoundView("nb-api", 1, remote.RoundClosed),
		stopWait: release,
	}
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1: the round runs on the node", got)
	}

	join := &hookJoin{}
	env.srv.stopFanoutHook = join.hook
	rt := env.runtime(t)
	res, err := relevo.Stop(ctx, rt, "api", relevo.StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "stopping" {
		t.Fatalf("Action = %q, want stopping", res.Action)
	}

	// The node saw the stop while its answer is still held: the head
	// returned first.
	tickUntil(t, func() bool { return script.stopHits.Load() == 1 }, "the node never saw the stop")
	if join.fired() {
		t.Fatal("fan-out completed before the node answered: the head blocked on it")
	}
	close(release)
	tickUntil(t, join.fired, "the fan-out never completed after the release")
	if err := join.first(); err != nil {
		t.Errorf("fan-out err = %v, want the landed stop", err)
	}

	back := loadBurstBinding(t, rt, "api")
	fwd := burstStoredForward(back)
	if fwd == nil || !fwd.StopRequested {
		t.Errorf("forward = %+v, want the stop stamped", back.Burst)
	}
	if back.Round != 1 {
		t.Errorf("Round = %d, want 1: the round stays open for the observe close", back.Round)
	}
}

// TestFanoutStopRowGating pins which rows a fan-out may dial: a verified row
// still taking work takes the stop, while a draining, unverified or gone row
// takes no new traffic -- the stop waits for the observe retry instead.
//
// Mutation check: drop any arm of the gate and that row's case dials.
func TestFanoutStopRowGating(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*burst.NodeRow)
		hits   int64
	}{
		{"ready", func(r *burst.NodeRow) {}, 1},
		{"draining", func(r *burst.NodeRow) { r.State = "draining" }, 0},
		{"unverified", func(r *burst.NodeRow) { r.Verified = false }, 0},
		{"gone", func(r *burst.NodeRow) { r.Gone = true }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Now().UTC()
			script := &nodeScript{
				stopStatus: 200, stopView: stubRoundView("nb-api", 1, remote.RoundClosed),
			}
			env, _, _ := stopFanoutSetup(t, script, &clock)
			env.srv.mu.Lock()
			tc.mutate(&env.srv.burstTable.Nodes[0])
			env.srv.mu.Unlock()
			rt := env.runtime(t)
			fwd := stampForward(t, rt, "api", "running")

			root, err := env.srv.ownerRoot(env.id)
			if err != nil {
				t.Fatalf("ownerRoot: %v", err)
			}
			join := &hookJoin{}
			env.srv.stopFanoutHook = join.hook
			env.srv.fanoutStopAsync(root, "api", fwd)
			tickUntil(t, join.fired, "the fan-out never completed")
			if got := script.stopHits.Load(); got != tc.hits {
				t.Errorf("node stop hits = %d, want %d", got, tc.hits)
			}
			if err := join.first(); err != nil {
				t.Errorf("fan-out err = %v, want nil: a gated row skips quietly", err)
			}
		})
	}
}

// TestFanoutStopFailureOnlyLogs pins the best-effort half: a node that
// refuses the stop leaves the stamped forward alone and only logs. The
// attempt still stamps the throttle, so the tick retries instead of
// redialling at once.
func TestFanoutStopFailureOnlyLogs(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{
		getStatus: 200, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		createStatus: 200,
		startStatus:  201, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
		stopStatus: 500, stopMsg: "node boom",
	}
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1: the round runs on the node", got)
	}

	join := &hookJoin{}
	env.srv.stopFanoutHook = join.hook
	rt := env.runtime(t)
	if _, err := relevo.Stop(ctx, rt, "api", relevo.StopOptions{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	tickUntil(t, join.fired, "the fan-out never completed")
	if err := join.first(); err == nil {
		t.Error("fan-out err = nil, want the node's 500 carried")
	}
	if got := script.stopHits.Load(); got != 1 {
		t.Errorf("node stop hits = %d, want exactly the one attempt", got)
	}

	back := loadBurstBinding(t, rt, "api")
	fwd := burstStoredForward(back)
	if fwd == nil || !fwd.StopRequested {
		t.Fatalf("forward = %+v, want the failed stop still stamped", back.Burst)
	}
	if back.Round != 1 {
		t.Errorf("Round = %d, want 1: a failed fan-out closes nothing", back.Round)
	}
	key := stopMarkKey(*fwd)
	if env.srv.burstHealth.stopDue(key, clock) {
		t.Error("stop due right after the failure, want the throttle to hold")
	}
	if !env.srv.burstHealth.stopDue(key, clock.Add(stopRetryEvery+time.Second)) {
		t.Error("stop not due past the interval, want the tick to retry")
	}
}

// TestStopSendingClosesRound pins the sending close: a stop stamped before
// the send reaches no node at all -- the send pass closes the round on the
// head with the queued local stop's entry and note -- and the binding takes
// the next round afterwards. No node contact means the scripted node answers
// nothing at all.
func TestStopSendingClosesRound(t *testing.T) {
	clock := time.Now().UTC()
	// The node would take the round: the passing run never asks it to.
	script := &nodeScript{
		getStatus: 200, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		createStatus: 200,
		startStatus:  201, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
	}
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	rt := env.runtime(t)
	stampForward(t, rt, "api", "sending")

	if got := env.srv.sendForwards(ctx, clock); got != 0 {
		t.Fatalf("confirmed = %d, want 0: a stopped send never ships", got)
	}
	if hits := script.hits.Load(); hits != 0 {
		t.Errorf("node hits = %d, want none: the head never contacts the node", hits)
	}

	back := loadBurstBinding(t, rt, "api")
	if back.Round != 2 {
		t.Fatalf("Round = %d, want 2: the round closed on the head", back.Round)
	}
	if burstStoredForward(back) != nil {
		t.Errorf("forward = %+v, want it cleared by the close", back.Burst.Forward)
	}
	if !back.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero: a close never requeues", back.QueuedAt)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindStop && e.Note == "stopped/dequeued" {
			found = true
		}
	}
	if !found {
		t.Errorf("log entries = %+v, want one stopped/dequeued", entries)
	}
	pending, found, err := rt.Store.PendingForMasterMind("api")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Note, "noreport stopped") {
		t.Errorf("report note = %q, want noreport stopped", pending.Note)
	}

	// The close leaves the binding placeable: the next queued round claims
	// a fresh forward instead of wedging on the stopped one.
	back.QueuedAt = clock
	if err := rt.Store.Save(back); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.placeForwards(clock); got != 1 {
		t.Fatalf("placed = %d, want 1: the next round is accepted", got)
	}
	again := loadBurstBinding(t, rt, "api")
	if fwd := burstStoredForward(again); fwd == nil || fwd.Phase != "sending" {
		t.Errorf("forward = %+v, want a fresh sending claim", again.Burst)
	}
}

// TestStopRequestedLostNodeCloses pins the lost close: a stop-requested
// running forward on a node that dropped off the table closes as a stop
// whose process is gone with it, instead of regaining the queue -- and the
// loss names no round back.
func TestStopRequestedLostNodeCloses(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{
		getStatus: 200, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		createStatus: 200,
		startStatus:  201, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
	}
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1: the round runs on the node", got)
	}
	rt := env.runtime(t)
	stampForward(t, rt, "api", "running")

	env.srv.mu.Lock()
	env.srv.burstTable = &burst.NodeTable{}
	env.srv.mu.Unlock()
	lost := &countingHooks{}
	env.srv.cfg.Hooks = lost
	env.srv.requeueLost(ctx, clock)

	back := loadBurstBinding(t, rt, "api")
	if back.Round != 2 {
		t.Fatalf("Round = %d, want 2: the lost round closed as stopped", back.Round)
	}
	if burstStoredForward(back) != nil {
		t.Errorf("forward = %+v, want it cleared by the close", back.Burst.Forward)
	}
	if !back.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero: a stopped round never regains the queue", back.QueuedAt)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindStop && e.Note == "stopped/gone" {
			found = true
		}
	}
	if !found {
		t.Errorf("log entries = %+v, want one stopped/gone", entries)
	}
	if got := lost.count(hooks.EventBurstNodeLost); got != 0 {
		t.Errorf("burst_node_lost = %d, want none: a stopped round is not a round back", got)
	}
}

// TestStopRequestedAdoptOnRestartCloses pins the adopt half: a sending
// forward the stop reached before a restart closes on adopt instead of
// returning to the queue. It never started, so it closes as dequeued.
func TestStopRequestedAdoptOnRestartCloses(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, _, _ := stopFanoutSetup(t, script, &clock)
	rt := env.runtime(t)
	stampForward(t, rt, "api", "sending")

	env.srv.mu.Lock()
	env.srv.burstTable = &burst.NodeTable{}
	env.srv.mu.Unlock()
	env.srv.adoptSending(context.Background())

	back := loadBurstBinding(t, rt, "api")
	if back.Round != 2 {
		t.Fatalf("Round = %d, want 2: the unconfirmed round closed as stopped", back.Round)
	}
	if burstStoredForward(back) != nil {
		t.Errorf("forward = %+v, want it cleared by the close", back.Burst.Forward)
	}
	if !back.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero: a stopped round never regains the queue", back.QueuedAt)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindStop && e.Note == "stopped/dequeued" {
			found = true
		}
	}
	if !found {
		t.Errorf("log entries = %+v, want one stopped/dequeued", entries)
	}
}

// stoppedStub is a scripted node whose round stays open and whose stop
// answers how the test says.
func stoppedStub(stopStatus int, stopCode remote.Code) *nodeScript {
	return &nodeScript{
		getStatus: 200, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		createStatus: 200,
		startStatus:  201, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
		stopStatus: stopStatus, stopView: stubRoundView("nb-api", 1, remote.RoundClosed),
		stopCode: stopCode, stopMsg: "nothing to stop",
	}
}

// TestStopReachesNodeAfterStartRace pins the race the fan-out cannot cover:
// a stop stamped while the send was mid-flight reaches the node on the next
// observe, and only once -- the landed stop ends the episode.
func TestStopReachesNodeAfterStartRace(t *testing.T) {
	clock := time.Now().UTC()
	script := stoppedStub(200, "")
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1: the round runs on the node", got)
	}
	rt := env.runtime(t)
	stampForward(t, rt, "api", "running")

	env.srv.observeForwards(ctx)
	if got := script.stopHits.Load(); got != 1 {
		t.Fatalf("node stop hits = %d, want 1: the race's stop goes out on the observe", got)
	}
	env.srv.observeForwards(ctx)
	if got := script.stopHits.Load(); got != 1 {
		t.Errorf("node stop hits = %d, want still 1: the landed stop ends the episode", got)
	}

	back := loadBurstBinding(t, rt, "api")
	if back.Round != 1 {
		t.Errorf("Round = %d, want 1: the retry only sends, the close comes later", back.Round)
	}
	if fwd := burstStoredForward(back); fwd == nil || fwd.Phase != "running" {
		t.Errorf("forward = %+v, want the round still out", back.Burst)
	}
}

// TestStopRetriedAfterFailure pins the failure half: a stop the node never
// answers is retried past the interval, and a nothing-to-stop answer ends
// the episode instead of redialling a round that will never open.
//
// Mutation check: remove the retry and the node sees no stop at all.
func TestStopRetriedAfterFailure(t *testing.T) {
	clock := time.Now().UTC()
	script := stoppedStub(500, "")
	script.stopMsg = "node boom"
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1: the round runs on the node", got)
	}
	rt := env.runtime(t)
	stampForward(t, rt, "api", "running")

	env.srv.observeForwards(ctx)
	if got := script.stopHits.Load(); got != 1 {
		t.Fatalf("node stop hits = %d, want 1: the first attempt goes out", got)
	}
	env.srv.observeForwards(ctx)
	if got := script.stopHits.Load(); got != 1 {
		t.Errorf("node stop hits = %d, want still 1: the failure waits out the interval", got)
	}

	script.stopStatus = 409
	script.stopCode = remote.CodeNothingToStop
	clock = clock.Add(stopRetryEvery + time.Second)
	env.srv.observeForwards(ctx)
	if got := script.stopHits.Load(); got != 2 {
		t.Fatalf("node stop hits = %d, want 2: the retry goes out past the interval", got)
	}
	clock = clock.Add(stopRetryEvery + time.Second)
	env.srv.observeForwards(ctx)
	if got := script.stopHits.Load(); got != 2 {
		t.Errorf("node stop hits = %d, want still 2: nothing-to-stop ends the episode", got)
	}
}

// TestBurstObserveClosesStoppedForward pins the unchanged close: a node
// round the node itself stopped closes on the head through the normal
// observe path, needing no report file -- the node's stopped view carries
// it -- and the head records no stop of its own.
func TestBurstObserveClosesStoppedForward(t *testing.T) {
	clock := time.Now().UTC()
	env, nodeSrv, _, fwd, nodeRoot := observeTestSetup(t, clock)
	ctx := context.Background()

	nodeRes, err := relevo.Stop(ctx, nodeSrv.runtimeAt(nodeRoot), fwd.NodeBinding, relevo.StopOptions{})
	if err != nil {
		t.Fatalf("node Stop: %v", err)
	}
	if nodeRes.Action != "killed" {
		t.Fatalf("node Action = %q, want killed: the node stops its own round", nodeRes.Action)
	}

	fake, ok := env.srv.burstProvider.(*recordingBurstProvider)
	if !ok {
		t.Fatalf("provider = %T, want the recording fake", env.srv.burstProvider)
	}
	tab := env.srv.copyBurstTable().Nodes[0]
	fake.resp = burst.ReportResponse{
		Version: burst.ReportVersion,
		Nodes: []burst.ReportNodeOut{{
			ID: "b1", Name: "b1 cx43@hel1", URL: tab.URL,
			Fingerprint: tab.Fingerprint, Cap: 2, State: "ready",
		}},
		ReportEvery: "30s",
	}
	env.srv.burstTick(ctx)

	rt := env.runtime(t)
	back, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if back.Round != 2 {
		t.Fatalf("head Round = %d, want 2 after the stopped close", back.Round)
	}
	if burstStoredForward(back) != nil {
		t.Errorf("head forward = %+v, want it cleared after the ack", back.Burst.Forward)
	}
	if _, err := os.Stat(rt.Store.ReportPath("api", 1)); !os.IsNotExist(err) {
		t.Errorf("head report present (err=%v), want none: the close rode the stopped view, not a report", err)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindStop {
			t.Errorf("head stop entry = %+v, want none: the stop happened on the node", e)
		}
	}
}

// The release fan-out: a settled done or unbind forgets the node bindings
// best-effort. These cases pin the detached send itself, the record shapes
// the verbs hand it, and the refusals that must fan out nothing.

// stubRowsEnv is a head with bursting on and two scripted node rows in the
// table, the shape a fan-out dials from. The clock the test drives is
// returned through the pointer.
func stubRowsEnv(t *testing.T, s1, s2 *nodeScript, clock *time.Time) (*testEnv, burst.NodeRow, burst.NodeRow) {
	t.Helper()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return *clock }
	})
	url1, fp1 := startStubNode(t, s1.serve())
	url2, fp2 := startStubNode(t, s2.serve())
	row1 := stubRow(t, "b1", url1, fp1)
	row2 := stubRow(t, "b2", url2, fp2)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row1, row2}}
	return env, row1, row2
}

// TestFanoutUnbindDetachedAndFailureOnlyLogs pins the send itself: one
// goroutine per node, the head's call returning before any node answers,
// and a node that refuses the unbind only reaching the log and the join
// hook -- never the release, which already settled.
func TestFanoutUnbindDetachedAndFailureOnlyLogs(t *testing.T) {
	clock := time.Now().UTC()
	release := make(chan struct{})
	held := &nodeScript{unbindWait: release}
	boom := &nodeScript{unbindStatus: http.StatusInternalServerError, unbindMsg: "node boom"}
	env, row1, row2 := stubRowsEnv(t, held, boom, &clock)
	join := &hookJoin{}
	env.srv.unbindFanoutHook = join.hook
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	env.srv.fanoutUnbindAsync(env.id, "api", []string{row1.ID, row2.ID})

	tickUntil(t, func() bool { return held.unbindHits.Load() == 1 && boom.unbindHits.Load() == 1 },
		"both nodes dialled while the call was already back")
	tickUntil(t, func() bool { return join.count() == 1 }, "the failing node's answer landed")
	if join.allNil() {
		t.Error("failing node landed nil, want its error carried to the join hook")
	}
	close(release)
	tickUntil(t, func() bool { return join.count() == 2 }, "the held node answered once released")
	if got := strings.Count(logs.String(), "node unbind fan-out failed"); got != 1 {
		t.Errorf("failure log lines = %d, want exactly the one failing node logged", got)
	}
}

// TestFanoutUnbindRowGating pins which rows a release may dial: a verified
// row still taking work takes the unbind, while a draining, unverified or
// gone row takes none -- the node's own release cleans that binding up.
//
// Mutation check: drop any arm of the gate and that row's case dials.
func TestFanoutUnbindRowGating(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*burst.NodeRow)
		hits   int64
	}{
		{"ready", func(r *burst.NodeRow) {}, 1},
		{"draining", func(r *burst.NodeRow) { r.State = "draining" }, 0},
		{"unverified", func(r *burst.NodeRow) { r.Verified = false }, 0},
		{"gone", func(r *burst.NodeRow) { r.Gone = true }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Now().UTC()
			script := &nodeScript{}
			env, row1, _ := stubRowsEnv(t, script, &nodeScript{}, &clock)
			env.srv.mu.Lock()
			tc.mutate(&env.srv.burstTable.Nodes[0])
			env.srv.mu.Unlock()
			join := &hookJoin{}
			env.srv.unbindFanoutHook = join.hook
			env.srv.fanoutUnbindAsync(env.id, "api", []string{row1.ID})
			tickUntil(t, join.fired, "the fan-out landed")
			if got := script.unbindHits.Load(); got != tc.hits {
				t.Errorf("node unbind hits = %d, want %d", got, tc.hits)
			}
			if !join.allNil() {
				t.Error("fan-out errored, want a gated row skipped quietly")
			}
		})
	}
}

// TestFanoutUnbindGoneIsSuccess pins the 404: a node that no longer holds
// the binding has already been unbound -- by this release through the
// drain, or by its own cleanup -- so the attempt lands instead of failing.
func TestFanoutUnbindGoneIsSuccess(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{unbindStatus: http.StatusNotFound, unbindMsg: "not found"}
	env, row1, _ := stubRowsEnv(t, script, &nodeScript{}, &clock)
	join := &hookJoin{}
	env.srv.unbindFanoutHook = join.hook
	env.srv.fanoutUnbindAsync(env.id, "api", []string{row1.ID})
	tickUntil(t, join.fired, "the fan-out landed")
	if !join.allNil() {
		t.Errorf("fan-out err = %v, want the node's 404 read as already-gone", join.first())
	}
}

// TestUnbindFanoutTargetsCases pins what a release may dial: every shipped
// node, the live forward's own node even for a sending claim that has no
// ship entry, deduplicated; a closed forward adds nothing because its node
// shipped, and a record without burst facts names nothing.
func TestUnbindFanoutTargetsCases(t *testing.T) {
	shipped := func(nodes ...string) map[string]string {
		m := make(map[string]string, len(nodes))
		for _, n := range nodes {
			m[n] = strings.Repeat("a", 40)
		}
		return m
	}
	fwd := func(node, phase string) *store.Forward {
		return &store.Forward{Node: node, NodeBinding: "nb-api", Round: 1, Phase: phase}
	}
	for _, tc := range []struct {
		name string
		b    store.Binding
		want []string
	}{
		{"no burst", store.Binding{}, nil},
		{"every shipped node", store.Binding{Burst: &store.BurstFacts{Shipped: shipped("b1", "b2")}},
			[]string{"b1", "b2"}},
		{"running forward node joins", store.Binding{Burst: &store.BurstFacts{
			Shipped: shipped("b1"), Forward: fwd("b2", "running")}}, []string{"b1", "b2"}},
		{"sending claim has no ship entry", store.Binding{Burst: &store.BurstFacts{
			Forward: fwd("b1", "sending")}}, []string{"b1"}},
		{"forward node already shipped is deduplicated", store.Binding{Burst: &store.BurstFacts{
			Shipped: shipped("b1"), Forward: fwd("b1", "running")}}, []string{"b1"}},
		{"closed forward adds nothing", store.Binding{Burst: &store.BurstFacts{
			Shipped: shipped("b1"), Forward: fwd("b2", "closed")}}, []string{"b1"}},
	} {
		got := slices.Clone(unbindFanoutTargets(tc.b))
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: targets = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// releasedRecordSetup is a head that shipped one confirmed round to the
// first scripted node and holds a ship entry for a second, the forward
// closed and the head round closed: the record done and unbind release
// from. It returns the env and the second node's script.
func releasedRecordSetup(t *testing.T, s1 *nodeScript, clock *time.Time) (*testEnv, *nodeScript) {
	t.Helper()
	env, _, _ := stopFanoutSetup(t, s1, clock)
	s2 := &nodeScript{}
	url2, fp2 := startStubNode(t, s2.serve())
	env.srv.mu.Lock()
	env.srv.burstTable.Nodes = append(env.srv.burstTable.Nodes, stubRow(t, "b2", url2, fp2))
	env.srv.mu.Unlock()
	if got := env.srv.sendForwards(context.Background(), *clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")
	f := burstStoredForward(b)
	if f == nil {
		t.Fatal("api has no forward to close")
	}
	f.Phase = "closed"
	b.Burst.Shipped["b2"] = strings.Repeat("b", 40)
	b.Burst.NodeBindings = append(b.Burst.NodeBindings, f.NodeBinding)
	b.Round = f.Round + 1
	if b.Serve == nil {
		b.Serve = &store.ServeFacts{}
	}
	b.Serve.ClosedRound = f.Round
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return env, s2
}

// sentScript is the node answer that takes a forward: an existing binding,
// a taken create, a started round.
func sentScript() *nodeScript {
	return &nodeScript{
		getStatus: 200, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		createStatus: 200,
		startStatus:  201, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
	}
}

// TestDoneFansOutAllNodeBindings pins the handler path: a done answers
// while a node's unbind is still held, both shipped nodes are dialled --
// not just the current one -- and the settled record keeps its closed
// forward while the ship facts it just released are cleared.
func TestDoneFansOutAllNodeBindings(t *testing.T) {
	clock := time.Now().UTC()
	release := make(chan struct{})
	s1 := sentScript()
	s1.unbindWait = release
	env, s2 := releasedRecordSetup(t, s1, &clock)
	join := &hookJoin{}
	env.srv.unbindFanoutHook = join.hook

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/done", nil, "")
	requireStatus(t, resp, body, http.StatusOK)

	tickUntil(t, func() bool { return s1.unbindHits.Load() == 1 && s2.unbindHits.Load() == 1 },
		"both node bindings dialled")
	close(release)
	tickUntil(t, func() bool { return join.count() == 2 }, "both fan-outs landed")
	if !join.allNil() {
		t.Error("a fan-out errored, want both unbinds landed")
	}

	rt := env.runtime(t)
	back := loadBurstBinding(t, rt, "api")
	if back.State != store.StateDone {
		t.Errorf("State = %q, want done", back.State)
	}
	if back.Burst == nil || len(back.Burst.Shipped) != 0 || len(back.Burst.NodeBindings) != 0 {
		t.Errorf("settled burst = %+v, want the ship facts cleared", back.Burst)
	}
	if f := back.Burst.Forward; f == nil || f.Phase != "closed" {
		t.Errorf("settled forward = %+v, want the closed one kept for its node's ack", back.Burst.Forward)
	}
}

// TestUnbindFansOutAllNodeBindings pins the same release at unbind: both
// shipped nodes are dialled after the record archives, without waiting.
func TestUnbindFansOutAllNodeBindings(t *testing.T) {
	clock := time.Now().UTC()
	s1 := sentScript()
	env, s2 := releasedRecordSetup(t, s1, &clock)
	join := &hookJoin{}
	env.srv.unbindFanoutHook = join.hook

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/unbind", nil, "")
	requireStatus(t, resp, body, http.StatusOK)

	rt := env.runtime(t)
	requireArchived(t, rt, "api")
	tickUntil(t, func() bool { return s1.unbindHits.Load() == 1 && s2.unbindHits.Load() == 1 },
		"both node bindings dialled")
	tickUntil(t, func() bool { return join.count() == 2 }, "both fan-outs landed")
	if !join.allNil() {
		t.Error("a fan-out errored, want both unbinds landed")
	}
}

// TestLiveForwardKeepsOpenRoundRule pins the open-round rule at the wire
// for a forwarded round: done answers the same 409 a local open round
// gets, an unforced unbind refuses, and neither dials a node -- the
// forward, not the binding list, owns that round. Only the forced release
// archives and dials the forward's node once.
func TestLiveForwardKeepsOpenRoundRule(t *testing.T) {
	clock := time.Now().UTC()
	script := sentScript()
	env, _, _ := stopFanoutSetup(t, script, &clock)
	if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	join := &hookJoin{}
	env.srv.unbindFanoutHook = join.hook
	rt := env.runtime(t)

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/done", nil, "")
	requireStatus(t, resp, body, http.StatusConflict)
	var eb remote.ErrorBody
	if err := json.Unmarshal(body, &eb); err != nil {
		t.Fatalf("decode done refusal: %v", err)
	}
	if eb.Code != remote.CodeRoundOpen {
		t.Errorf("done code = %q, want %q", eb.Code, remote.CodeRoundOpen)
	}
	if got := script.unbindHits.Load(); got != 0 || join.count() != 0 {
		t.Errorf("hits = %d, hook = %d, want a refused done to fan out nothing", got, join.count())
	}
	kept := loadBurstBinding(t, rt, "api")
	if burstStoredForward(kept) == nil || len(kept.Burst.Shipped) == 0 {
		t.Errorf("record burst = %+v, want the live forward and ship facts untouched", kept.Burst)
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/unbind", nil, "")
	requireStatus(t, resp, body, http.StatusConflict)
	if err := json.Unmarshal(body, &eb); err != nil {
		t.Fatalf("decode unbind refusal: %v", err)
	}
	if eb.Code != remote.CodeRoundOpen {
		t.Errorf("unbind code = %q, want %q", eb.Code, remote.CodeRoundOpen)
	}
	if got := script.unbindHits.Load(); got != 0 {
		t.Errorf("hits = %d, want still none on a refused unbind", got)
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/unbind?force=1", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	tickUntil(t, func() bool { return script.unbindHits.Load() == 1 }, "the forward's node dialled once")
	tickUntil(t, func() bool { return join.count() == 1 }, "the unbind fan-out landed")
	if !join.allNil() {
		t.Error("fan-out errored, want the node's unbind landed")
	}
	requireArchived(t, rt, "api")
}

// TestNeedsYouStopsNodeAfterSave pins the ordering: clearing a NEEDS YOU
// forward stops the node's round, but only once the head's save landed. A
// save that failed must leave the node untouched -- the head still thinks
// the round runs -- while the parked shape that did save dials exactly one
// stop for the cleared forward's (node, binding, round).
func TestNeedsYouStopsNodeAfterSave(t *testing.T) {
	needsYouSetup := func(t *testing.T, script *nodeScript) (*testEnv, relevo.Runtime) {
		t.Helper()
		clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		url, fp := startStubNode(t, script.serve())
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{stubRow(t, "b1", url, fp)}}
		saveHeadForward(t, env, "api", "b1", "nb-api", 1, clock.Add(-time.Minute))
		rt := env.runtime(t)
		b := loadBurstBinding(t, rt, "api")
		b.Burst.Forward.Phase = "running"
		if b.Burst.Shipped == nil {
			b.Burst.Shipped = map[string]string{}
		}
		b.Burst.Shipped["b1"] = strings.Repeat("a", 40)
		b.Burst.NodeBindings = append(b.Burst.NodeBindings, "nb-api")
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		return env, rt
	}

	t.Run("the parked save lands and the node sees one stop", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusOK,
			getView: remote.BindingView{Name: "nb-api", Round: 1,
				RoundState: remote.RoundNeedsYou, Halt: "node waits on its human"},
			stopStatus: http.StatusOK, stopView: stubRoundView("nb-api", 1, remote.RoundClosed),
		}
		env, rt := needsYouSetup(t, script)
		join := &hookJoin{}
		env.srv.stopFanoutHook = join.hook
		if got := env.srv.observeForwards(context.Background()); got != 0 {
			t.Fatalf("observe closed = %d, want 0 for a parked round", got)
		}
		tickUntil(t, join.fired, "the parked round's stop fan-out landed")
		if !join.allNil() {
			t.Errorf("fan-out err = %v, want the stop landed", join.first())
		}
		if got := script.stopHits.Load(); got != 1 {
			t.Errorf("node stop hits = %d, want exactly one", got)
		}
		back := loadBurstBinding(t, rt, "api")
		if back.State != store.StateNeedsYou {
			t.Errorf("head state = %q, want needs_you", back.State)
		}
		if burstStoredForward(back) != nil {
			t.Errorf("head forward = %+v, want it cleared", back.Burst.Forward)
		}
	})

	t.Run("a save that fails stops nothing", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusOK,
			getView: remote.BindingView{Name: "nb-api", Round: 1,
				RoundState: remote.RoundNeedsYou, Halt: "node waits on its human"},
			stopStatus: http.StatusOK, stopView: stubRoundView("nb-api", 1, remote.RoundClosed),
		}
		env, rt := needsYouSetup(t, script)
		// A second writer on the same tree refuses every save of api, so
		// the park's save fails while its load and fetch succeed.
		back := loadBurstBinding(t, rt, "api")
		collide := store.Binding{Name: "other", CWD: back.CWD, State: store.StateActive, Round: 1}
		raw, err := json.Marshal(collide)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.srv.cfg.DB.RecordPut(db.Record{Owner: string(env.id), Name: "other", Round: 1, JSON: string(raw)}); err != nil {
			t.Fatalf("seed colliding record: %v", err)
		}
		ctx := context.Background()
		env.srv.observeForwards(ctx)
		env.srv.observeForwards(ctx)
		if got := script.stopHits.Load(); got != 0 {
			t.Errorf("node stop hits = %d, want none: a failed save must not stop a round the head still tracks", got)
		}
		kept := loadBurstBinding(t, rt, "api")
		if f := burstStoredForward(kept); f == nil || f.Phase != "running" {
			t.Errorf("head forward = %+v, want the running claim kept after the failed park", kept.Burst.Forward)
		}
	})
}

// TestNeedsYouNextRoundSameNode pins the goal's other half against a real
// node: after the parked round is cleared and stopped there, the head
// answers and places its next round on the same node -- whose binding
// takes the round despite the stopped prior one.
func TestNeedsYouNextRoundSameNode(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, nodeSrv, _, fwd, nodeRoot := observeTestSetup(t, clock)
	ctx := context.Background()
	nst := nodeSrv.ownerStore(nodeRoot)
	nb, err := nst.Load(fwd.NodeBinding)
	if err != nil {
		t.Fatal(err)
	}
	nb.State = store.StateNeedsYou
	nb.Halt = "the builder here waits on its human"
	if err := nst.Save(nb); err != nil {
		t.Fatal(err)
	}

	if got := env.srv.observeForwards(ctx); got != 0 {
		t.Fatalf("observe closed = %d, want 0 for a parked round", got)
	}
	rt := env.runtime(t)
	tickUntil(t, func() bool {
		return burstStoredForward(loadBurstBinding(t, rt, "api")) == nil
	}, "the head parked and cleared the forward")
	tickUntil(t, func() bool {
		c, err := nst.Load(fwd.NodeBinding)
		return err == nil && c.Serve != nil && c.Serve.ClosedRound >= 1
	}, "the node closed the round the stop reached")

	if _, err := relevo.Stop(ctx, rt, "api", relevo.StopOptions{}); err != nil {
		t.Fatalf("head stop: %v", err)
	}
	bundle := snapshotRef(t, env, env.clientDir, "refs/relevo/api/out")
	formBytes, ct := makeRoundForm(t, 2, "# Plan for api round 2", bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireCreated(t, resp, body, "api round 2")
	if got := env.srv.placeForwards(clock.Add(time.Minute)); got != 1 {
		t.Fatalf("placed = %d, want 1: the next round claims the same node", got)
	}
	// The claim names its binding the placement rule does; the node's
	// standing binding carries the renamed one the setup sent it under.
	claimed := loadBurstBinding(t, rt, "api")
	cf := burstStoredForward(claimed)
	cf.NodeBinding = fwd.NodeBinding
	claimed.Burst.Forward = cf
	if err := rt.Store.Save(claimed); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.sendForwards(ctx, clock.Add(2*time.Minute)); got != 1 {
		t.Fatalf("confirmed = %d, want 1: the node binding takes the round after its stopped one", got)
	}
	f := burstStoredForward(loadBurstBinding(t, rt, "api"))
	if f == nil || f.Round != 2 || f.Phase != "running" || f.Node != "b1" {
		t.Errorf("forward = %+v, want a running round 2 on b1", f)
	}
}

// TestDoneThenResumeForwardsAgain pins that done forgets what it unbound
// even when the best-effort unbind fails: the node keeps its old binding,
// the settled record clears its ship facts, and a resume plus the next
// round ship the full bundle from the empty base to that standing binding
// -- a surviving ship base would have cut an incremental bundle from a
// commit the release was meant to forget.
func TestDoneThenResumeForwardsAgain(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{
		getStatus: 200, getView: stubRoundView("nb-api", 1, remote.RoundClosed),
		startStatus: 201, startView: stubRoundView("nb-api", 2, remote.RoundRunning),
		unbindStatus: http.StatusInternalServerError, unbindMsg: "node boom",
	}
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	// The round came home and closed; the forward stands closed, owed its
	// node's ack: the shape a done may not half-forget.
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")
	f := burstStoredForward(b)
	if f == nil {
		t.Fatal("api has no forward to close")
	}
	f.Phase = "closed"
	b.Round = f.Round + 1
	if b.Serve == nil {
		b.Serve = &store.ServeFacts{}
	}
	b.Serve.ClosedRound = f.Round
	b.Serve.AckedRound = f.Round
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	join := &hookJoin{}
	env.srv.unbindFanoutHook = join.hook
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/done", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	tickUntil(t, join.fired, "the failed unbind attempt landed")
	if join.allNil() {
		t.Error("the node's refusal landed nil, want it carried -- logged only, never fatal")
	}
	if got := script.unbindHits.Load(); got != 1 {
		t.Errorf("node unbind hits = %d, want the one attempt", got)
	}
	back := loadBurstBinding(t, rt, "api")
	if back.Burst == nil || len(back.Burst.Shipped) != 0 || len(back.Burst.NodeBindings) != 0 {
		t.Errorf("settled burst = %+v, want the ship facts cleared even though the unbind failed", back.Burst)
	}
	if hf := burstStoredForward(back); hf == nil || hf.Phase != "closed" {
		t.Fatalf("forward = %+v, want the closed one kept for its ack", back.Burst.Forward)
	}

	// The human resumes the binding and sends the next round; the closed
	// forward's ack reaches the standing binding and clears it (the stub
	// has no ack route: its 404 means nothing left to ack), so the next
	// round can claim the same node again.
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/resume", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	bundle := snapshotRef(t, env, env.clientDir, "refs/relevo/api/out")
	formBytes, ct := makeRoundForm(t, 2, "# Plan for api round 2", bundle)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireCreated(t, resp, body, "api round 2")
	w := observeWork{ownerPath: headOwnerPath(t, env), name: "api", fwd: *burstStoredForward(loadBurstBinding(t, rt, "api"))}
	cl, err := env.srv.burstNodeClient([]burst.NodeRow{env.srv.copyBurstTable().Nodes[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !env.srv.ackObserveClosed(ctx, cl, w) {
		t.Fatal("ack-not-found kept the closed forward, want it cleared")
	}

	if got := env.srv.placeForwards(clock.Add(time.Minute)); got != 1 {
		t.Fatalf("placed = %d, want 1: the resumed binding forwards again", got)
	}
	claimed := loadBurstBinding(t, rt, "api")
	cf := burstStoredForward(claimed)
	cf.NodeBinding = "nb-api" // the standing binding the failed unbind left behind
	claimed.Burst.Forward = cf
	if err := rt.Store.Save(claimed); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.sendForwards(ctx, clock.Add(2*time.Minute)); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	if !script.lastStartHasBundle() {
		t.Error("the re-forward shipped no bundle, want the full bundle from the cleared base")
	}
	f2 := burstStoredForward(loadBurstBinding(t, rt, "api"))
	if f2 == nil || f2.Phase != "running" || f2.Round != 2 {
		t.Errorf("forward = %+v, want round 2 running again", f2)
	}
}

// TestDoneWithClosedForwardClearsOnAckNotFound pins the released node's
// ack: a done keeps the closed forward and fans the unbind out, the node
// answers 404 -- already gone -- and the unbind lands. The ack that then
// reaches that gone binding clears the forward without counting a failure
// and without an ack-stuck episode: there is nothing left to ack.
func TestDoneWithClosedForwardClearsOnAckNotFound(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	hookEvents := &countingHooks{}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
		cfg.Hooks = hookEvents
	})
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, remote.ErrorBody{Code: remote.CodeNotFound, Message: "not found"})
	})
	url, fp := startStubNode(t, notFound)
	row := stubRow(t, "b1", url, fp)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
	since := clock.Add(-time.Minute)
	saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")
	b.Burst.Forward.Phase = "closed"
	b.Burst.Shipped = map[string]string{"b1": strings.Repeat("a", 40)}
	b.Burst.NodeBindings = []string{"nb-api"}
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	join := &hookJoin{}
	env.srv.unbindFanoutHook = join.hook
	if _, err := relevo.Done(context.Background(), rt, "api"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	tickUntil(t, join.fired, "the unbind fan-out landed")
	if !join.allNil() {
		t.Errorf("fan-out err = %v, want the node's 404 read as already-gone", join.first())
	}
	back := loadBurstBinding(t, rt, "api")
	if f := burstStoredForward(back); f == nil || f.Phase != "closed" {
		t.Fatalf("forward = %+v, want the closed one kept for its ack", back.Burst.Forward)
	}

	w := observeWork{ownerPath: headOwnerPath(t, env), name: "api", fwd: *burstStoredForward(back)}
	cl, err := env.srv.burstNodeClient([]burst.NodeRow{row})
	if err != nil {
		t.Fatal(err)
	}
	if !env.srv.ackObserveClosed(context.Background(), cl, w) {
		t.Fatal("ack-not-found kept the forward, want it cleared: no node binding is left to ack")
	}
	cleared := loadBurstBinding(t, rt, "api")
	if burstStoredForward(cleared) != nil {
		t.Errorf("forward = %+v, want it cleared with nothing to ack", burstStoredForward(cleared))
	}
	if got := hookEvents.count(hooks.EventBurstAckStuck); got != 0 {
		t.Errorf("burst_ack_stuck = %d, want none for a binding the release already removed", got)
	}
}

// TestCollectSettledReleasesStaleShipFacts pins the cleanup path: a
// settled binding normally arrives with its ship facts already cleared by
// its done and dials nothing, but a record whose ship facts outlive their
// unbinds still gets those unbinds fanned out by the sweep's release.
func TestCollectSettledReleasesStaleShipFacts(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, _, _ := stubRowsEnv(t, script, &nodeScript{}, &clock)
	saveHeadForward(t, env, "api", "b1", "nb-api", 1, clock.Add(-time.Minute))
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")
	b.State = store.StateDone
	b.QueuedAt = time.Time{}
	b.Burst.Forward = nil
	b.Burst.Shipped = map[string]string{"b1": strings.Repeat("a", 40)}
	b.Burst.NodeBindings = []string{"nb-api"}
	b.Worktree = ""
	b.Round = 2
	b.Serve = &store.ServeFacts{ClosedRound: 1, AckedRound: 1}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(ackedGrace + time.Minute)
	collected, err := env.srv.collectSettled(context.Background())
	if err != nil {
		t.Fatalf("collectSettled: %v", err)
	}
	if collected != 1 {
		t.Fatalf("collected = %d, want the one settled binding", collected)
	}
	tickUntil(t, func() bool { return script.unbindHits.Load() == 1 },
		"the stale ship entry's node dialled")
	requireArchived(t, rt, "api")
}
