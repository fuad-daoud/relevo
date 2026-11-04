package serve

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
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
