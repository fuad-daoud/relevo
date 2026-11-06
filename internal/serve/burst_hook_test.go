package serve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// recordingBurstProvider is a burst.Provider that records every request and
// answers from fields the test sets. It stands in for the provider command,
// which never runs in tests: no test may spawn a stubbed network call.
type recordingBurstProvider struct {
	mu   sync.Mutex
	reqs []burst.ReportRequest
	resp burst.ReportResponse
	err  error
}

func (f *recordingBurstProvider) Report(req burst.ReportRequest) (burst.ReportResponse, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return f.resp, nil, f.err
}

func (f *recordingBurstProvider) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *recordingBurstProvider) last() burst.ReportRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

// burstHookEnv is a server with bursting on, a controllable clock and the
// given provider and cap.
func burstHookEnv(t *testing.T, clock *time.Time, provider burst.Provider, maxBuilders int) *testEnv {
	t.Helper()
	return setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = []byte("pem-client-key")
		cfg.Provider = provider
		cfg.MaxBuilders = maxBuilders
		cfg.Now = func() time.Time { return *clock }
	})
}

// burstEligibleRow is one ready, verified node with a free seat.
func burstEligibleRow(id string, cap int) burst.NodeRow {
	return burst.NodeRow{
		ID: id, Name: id + " cx43@hel1", URL: "https://" + id + ".example",
		Fingerprint: burstTestFingerprint, Cap: cap, State: "ready",
		Verified: true, LastSeen: time.Date(2026, 11, 2, 8, 59, 0, 0, time.UTC),
	}
}

// saveBurstBinding stores a binding directly: pid non-zero means a running
// headless builder, queuedAt non-zero means a queued round.
func saveBurstBinding(t *testing.T, rt relevo.Runtime, name string, queuedAt time.Time, pid int) {
	t.Helper()
	cwd := filepath.Join(t.TempDir(), name+"-wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	b := store.Binding{
		Name: name, CWD: cwd, State: store.StateActive, Round: 1,
		QueuedAt: queuedAt, BuilderCandidate: "claude/anthropic/haiku",
	}
	if pid != 0 {
		b.Builder = store.Endpoint{Mode: store.ModeHeadless, PID: pid, StartedAt: time.Now().Unix()}
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

func loadBurstBinding(t *testing.T, rt relevo.Runtime, name string) store.Binding {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return b
}

// TestBurstPlaceFillsLocalFirst pins that placement runs after admit: with a
// free local seat and an eligible node, the first queued round is the head's
// and stays queued, while the next is claimed for the node as a sending
// forward that keeps its FIFO place.
func TestBurstPlaceFillsLocalFirst(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	fake := &recordingBurstProvider{}
	env := burstHookEnv(t, &clock, fake, 2)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{burstEligibleRow("b1", 2)}}
	rt := env.runtime(t)
	first := clock.Add(-time.Minute)
	next := first.Add(2 * time.Second)
	saveBurstBinding(t, rt, "a", time.Time{}, 4242)
	saveBurstBinding(t, rt, "b", first, 0)
	saveBurstBinding(t, rt, "c", next, 0)

	if got := env.srv.placeForwards(clock); got != 1 {
		t.Fatalf("placed = %d, want 1", got)
	}
	kept := loadBurstBinding(t, rt, "b")
	if burstStoredForward(kept) != nil {
		t.Errorf("b forward = %+v, want the first queued round left for the local seat", kept.Burst.Forward)
	}
	if !kept.QueuedAt.Equal(first) {
		t.Errorf("b QueuedAt = %v, want %v", kept.QueuedAt, first)
	}
	moved := loadBurstBinding(t, rt, "c")
	fwd := burstStoredForward(moved)
	if fwd == nil {
		t.Fatal("c forward = nil, want the next queued round claimed for the node")
	}
	if fwd.Node != "b1" || fwd.Phase != "sending" || fwd.Round != 1 {
		t.Errorf("c forward = %+v, want node b1 phase sending round 1", fwd)
	}
	if !fwd.ClaimedAt.Equal(clock) || !fwd.QueuedSince.Equal(next) || !moved.QueuedAt.IsZero() {
		t.Errorf("c claim = claimedAt %v queuedSince %v queuedAt %v, want %v %v zero",
			fwd.ClaimedAt, fwd.QueuedSince, moved.QueuedAt, clock, next)
	}
	if want := burstNodeBinding(env.id, "c"); fwd.NodeBinding != want {
		t.Errorf("c node binding = %q, want %q", fwd.NodeBinding, want)
	}
}

// TestBurstWaitingSkipsQueuedPlanner pins that a queued planner never reaches
// the provider: burstWaiting leaves it out of the waiting list and the
// placement index, placeForwards claims only the queued builder's seat, and
// the head's reported queued load counts builder seats only.
func TestBurstWaitingSkipsQueuedPlanner(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	fake := &recordingBurstProvider{resp: burstSuccessResponse()}
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = []byte("pem-client-key")
		cfg.Provider = fake
		cfg.MaxBuilders = 2
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
		cfg.Now = func() time.Time { return clock }
	})
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{burstEligibleRow("b1", 2)}}
	rt := env.runtime(t)
	first := clock.Add(-time.Minute)
	saveBurstBinding(t, rt, "a", time.Time{}, 4242)
	saveBurstBinding(t, rt, "b", first, 0)
	saveBurstBinding(t, rt, "c", first.Add(2*time.Second), 0)
	stampRole(t, rt, "c", "lite-planner")

	c, _ := env.srv.census()
	if got := c.plannersQueued(); got != 1 {
		t.Fatalf("planners queued = %d, want 1 (the c binding)", got)
	}
	waiting, index := env.srv.burstWaiting(c)
	if len(waiting) != 1 {
		t.Fatalf("waiting = %+v, want only the queued builder", waiting)
	}
	if want := string(env.id) + "/b"; waiting[0].ID != want {
		t.Errorf("waiting[0].ID = %q, want %q", waiting[0].ID, want)
	}
	if _, ok := index[string(env.id)+"/c"]; ok {
		t.Error("the queued planner has a placement index entry, want none")
	}

	if got := env.srv.placeForwards(clock); got != 0 {
		t.Fatalf("placed = %d, want 0: the queued builder is the head's own seat", got)
	}
	for _, name := range []string{"b", "c"} {
		if kept := loadBurstBinding(t, rt, name); burstStoredForward(kept) != nil {
			t.Errorf("%s forward = %+v, want nothing placed", name, kept.Burst.Forward)
		} else if kept.QueuedAt.IsZero() {
			t.Errorf("%s lost its queue time, want it kept", name)
		}
	}

	env.srv.burstTick(context.Background())
	if got := fake.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	req := fake.last()
	checkBurstRequest(t, req, 1, 1)
	if req.Local != (burst.ReportLocal{Running: 1, Queued: 1, Cap: 2}) {
		t.Errorf("local = %+v, want running 1 queued 1 cap 2 with the planner left out", req.Local)
	}
	for _, w := range req.Waiting {
		if strings.HasSuffix(w.ID, "/c") {
			t.Errorf("waiting carries the queued planner: %+v", req.Waiting)
		}
	}
}

// burstSuccessResponse keeps the one test node live at the reported cadence.
func burstSuccessResponse() burst.ReportResponse {
	return burst.ReportResponse{
		Version: burst.ReportVersion,
		Nodes: []burst.ReportNodeOut{{
			ID: "b1", Name: "b1 cx43@hel1", URL: "https://b1.example",
			Fingerprint: burstTestFingerprint, Cap: 2, State: "ready",
		}},
		ReportEvery: "30s",
	}
}

// checkBurstRequest pins the stdin contract: a validated v2 document with the
// waiting list and every live node, all-zero ones included.
func checkBurstRequest(t *testing.T, req burst.ReportRequest, waiting, nodes int) {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	got, notes, err := burst.ValidateRequest(data)
	if err != nil {
		t.Fatalf("ValidateRequest: %v (notes %q)", err, notes)
	}
	if got.Version != burst.ReportVersion {
		t.Errorf("version = %d, want %d", got.Version, burst.ReportVersion)
	}
	if got.Head.Name == "" {
		t.Error("head.name is empty, want the head named")
	}
	if got.Waiting == nil || len(got.Waiting) != waiting {
		t.Errorf("waiting = %+v, want %d entries and never null", got.Waiting, waiting)
	}
	if got.Nodes == nil || len(got.Nodes) != nodes {
		t.Fatalf("nodes = %+v, want %d entries and never null", got.Nodes, nodes)
	}
}

// TestBurstReportCycleContract pins the report loop: the provider sees a
// validated v2 request carrying the waiting rounds and every live node at
// zero, a failed call keeps the last good list with its error and waits a
// full interval, and a forward still reports out of cycle.
func TestBurstReportCycleContract(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	fake := &recordingBurstProvider{resp: burstSuccessResponse()}
	env := burstHookEnv(t, &clock, fake, 1)
	row := burstEligibleRow("b1", 2)
	row.Verified = false
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
	rt := env.runtime(t)
	saveBurstBinding(t, rt, "a", time.Time{}, 4242)
	saveBurstBinding(t, rt, "b", clock.Add(-time.Minute), 0)

	env.srv.burstTick(context.Background())
	if got := fake.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	req := fake.last()
	checkBurstRequest(t, req, 1, 1)
	if req.Reason != burst.ReportReasonStartup {
		t.Errorf("reason = %q, want startup on the first report", req.Reason)
	}
	if req.Local != (burst.ReportLocal{Running: 1, Queued: 1, Cap: 1}) {
		t.Errorf("local = %+v, want running 1 queued 1 cap 1", req.Local)
	}
	n := req.Nodes[0]
	if n.ID != "b1" || n.Running != 0 || n.Queued != 0 || n.Unacked != 0 {
		t.Errorf("node = %+v, want the live b1 at zero", n)
	}
	table := env.srv.copyBurstTable()
	if !table.LastReport.Equal(clock) || table.Interval != 30*time.Second || table.LastErr != "" {
		t.Errorf("table after success = %+v, want the report mark, a 30s cadence and no error", table)
	}

	fake.err = errors.New("provider boom")
	clock = clock.Add(31 * time.Second)
	env.srv.burstTick(context.Background())
	if got := fake.calls(); got != 2 {
		t.Fatalf("provider calls after the interval = %d, want 2", got)
	}
	table = env.srv.copyBurstTable()
	if table.LastErr == "" || !strings.Contains(table.LastErr, "boom") {
		t.Errorf("last_err = %q, want the failure recorded", table.LastErr)
	}
	if len(table.Nodes) != 1 || table.Nodes[0].Gone || table.Interval != 30*time.Second {
		t.Errorf("table after failure = %+v, want the last good list kept", table)
	}
	if !table.LastReport.Equal(clock) {
		t.Errorf("LastReport = %v, want the failed attempt to cost a full interval from %v", table.LastReport, clock)
	}

	env.srv.burstTick(context.Background())
	if got := fake.calls(); got != 2 {
		t.Errorf("provider calls without an interval passing = %d, want still 2", got)
	}

	fake.err = nil
	fake.resp = burstSuccessResponse()
	env.srv.mu.Lock()
	env.srv.burstTable.Nodes[0].Verified = true
	env.srv.mu.Unlock()
	env.srv.burstTick(context.Background())
	if got := fake.calls(); got != 3 {
		t.Fatalf("provider calls after a forward = %d, want 3: a forward reports out of cycle", got)
	}
	if got := fake.last().Reason; got != burst.ReportReasonForwarded {
		t.Errorf("reason = %q, want forwarded once a round placed", got)
	}
	moved := loadBurstBinding(t, rt, "b")
	if fwd := burstStoredForward(moved); fwd == nil || fwd.Node != "b1" {
		t.Errorf("b forward = %+v, want the queued round claimed on b1", moved.Burst)
	}
	if table := env.srv.copyBurstTable(); table.LastErr != "" {
		t.Errorf("last_err = %q, want the good answer to clear it", table.LastErr)
	}
}

// TestBurstOffPlacesNothing pins the hook's gate: with bursting off (no
// provider) a queued round past the cap is never claimed and keeps its queue
// time, even when the table names an eligible node.
func TestBurstOffPlacesNothing(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.MaxBuilders = 1
	})
	if env.srv.burstProvider != nil {
		t.Fatal("burst provider resolved with no client key, want bursting off")
	}
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{burstEligibleRow("b1", 2)}}
	ownerB := addOwner(t, env, "bob")

	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, resp, body, "A")
	resp, body = sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, resp, body, "B")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	kept, err := testRuntime(t, env.srv, ownerB.id).Store.Load("api")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if burstStoredForward(kept) != nil {
		t.Errorf("B forward = %+v, want nothing placed while bursting is off", kept.Burst.Forward)
	}
	if kept.QueuedAt.IsZero() {
		t.Error("B QueuedAt is zero, want the queue time kept")
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundQueued)
}
