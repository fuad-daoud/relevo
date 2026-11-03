package serve

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// burstKeepRow is the provider answer that keeps one table row exactly as
// listed: an empty answer would mark it gone on the in-tick merge and the
// tick would requeue for disappearance, not for the problem under test.
func burstKeepRow(row burst.NodeRow) burst.ReportResponse {
	return burst.ReportResponse{
		Version: burst.ReportVersion,
		Nodes: []burst.ReportNodeOut{{
			ID: row.ID, Name: row.Name, URL: row.URL,
			Fingerprint: row.Fingerprint, Cap: row.Cap, State: row.State,
		}},
		ReportEvery: "30s",
	}
}

// TestBurstForwardRoundTripEndToEnd runs the whole forwarded-round chain
// over httptest TLS with the script runner behind the node: place, send,
// the node builds and closes, then one head tick fetches the round, closes
// the head side, and acks -- the node records the ack, the head carries the
// node's work, and the owed report says closed.
func TestBurstForwardRoundTripEndToEnd(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, nodeSrv, runner, fwd, nodeRoot := observeTestSetup(t, clock)
	nodeCommit := closeNodeRound(t, nodeSrv, runner, nodeRoot, fwd.NodeBinding, 1)

	fake := env.srv.burstProvider.(*recordingBurstProvider)
	fake.resp = burstKeepRow(env.srv.copyBurstTable().Nodes[0])

	ctx := context.Background()
	env.srv.burstTick(ctx)

	rt := env.runtime(t)
	back, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if back.Serve == nil || back.Serve.ClosedRound != 1 || back.Round != 2 {
		t.Fatalf("head = closed %+v round %d, want round 1 closed and the round advanced", back.Serve, back.Round)
	}
	if burstStoredForward(back) != nil {
		t.Errorf("head forward = %+v, want it cleared by the ack", back.Burst.Forward)
	}
	sha, ok, err := env.srv.cfg.Git.RefSHA(ctx, back.Serve.BareRepo, "refs/heads/relevo/api")
	if err != nil || !ok || sha != nodeCommit {
		t.Errorf("head branch = %q (ok=%v err=%v), want the node commit %q", sha, ok, err, nodeCommit)
	}
	report, err := os.ReadFile(rt.Store.ReportPath("api", 1))
	if err != nil {
		t.Fatalf("fetched report: %v", err)
	}
	if len(report) == 0 {
		t.Error("fetched report is empty, want the node's report bytes")
	}
	nodeBack, err := nodeSrv.ownerStore(nodeRoot).Load(fwd.NodeBinding)
	if err != nil {
		t.Fatal(err)
	}
	if nodeBack.Serve == nil || nodeBack.Serve.AckedRound != 1 {
		t.Errorf("node AckedRound = %+v, want 1: the ack did not land end to end", nodeBack.Serve)
	}
	if got := fake.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want the close to report in its tick", got)
	}
	if got := fake.last().Reason; got != burst.ReportReasonClosed {
		t.Errorf("reason = %q, want closed", got)
	}
}

// TestBurstSilentNodeRequeuesItsRoundAtFIFOPlace runs the loss path end to
// end: a verified node that confirmed a round stops answering, the observe
// pass marks the row unreachable, and once the loss passes
// serve.burst.node_lost_after the tick puts the round back in the local
// queue at the FIFO place it held before the claim -- before the node ever
// reappears in the provider's answer as gone.
func TestBurstSilentNodeRequeuesItsRoundAtFIFOPlace(t *testing.T) {
	// The clock is frozen from this instant and the head signs its own
	// client requests with it, so the start stays inside the nonce window.
	clock := time.Now().UTC()
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
		cfg.Now = func() time.Time { return clock }
		cfg.Hooks = hookEvents
	})
	fake := env.srv.burstProvider.(*recordingBurstProvider)
	ctx := context.Background()
	_, nodeTS, nodeFP := startBurstNodeWithRunner(t, &headKP)
	row := stubRow(t, "b1", nodeTS.URL, nodeFP)
	fwd := queueForward(t, env, "api", row, clock)
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	fake.resp = burstKeepRow(row)

	nodeTS.Close()
	clock = clock.Add(time.Minute)
	env.srv.burstTick(ctx)

	tab := env.srv.copyBurstTable().Nodes[0]
	if tab.Problem != burst.ReportProblemUnreachable || tab.ProblemSince.IsZero() {
		t.Fatalf("row = problem %q since %v, want the silent node marked unreachable by observe", tab.Problem, tab.ProblemSince)
	}
	rt := env.runtime(t)
	kept := loadBurstBinding(t, rt, "api")
	if have := burstStoredForward(kept); have == nil || have.Phase != "running" {
		t.Fatalf("forward = %+v, want the round still out inside the loss bound", kept.Burst)
	}
	if got := hookEvents.count(hooks.EventBurstNodeLost); got != 0 {
		t.Fatalf("burst_node_lost = %d, want nothing requeued before the bound", got)
	}

	// Past the ten-minute bound the round comes home at its old queue place.
	clock = clock.Add(11 * time.Minute)
	env.srv.burstTick(ctx)

	back := loadBurstBinding(t, rt, "api")
	if burstStoredForward(back) != nil {
		t.Errorf("forward = %+v, want the lost node's round cleared", back.Burst.Forward)
	}
	if !back.QueuedAt.Equal(fwd.QueuedSince) {
		t.Errorf("QueuedAt = %v, want the FIFO place %v the round held before the claim", back.QueuedAt, fwd.QueuedSince)
	}
	if back.State != store.StateActive {
		t.Errorf("state = %q, want the round waits as an active binding", back.State)
	}
	if got := hookEvents.count(hooks.EventBurstNodeLost); got != 1 {
		t.Errorf("burst_node_lost = %d, want one naming the lost node", got)
	}
	// The requeued round is not re-placed on the troubled row: an
	// unreachable node takes nothing.
	if got := env.srv.copyBurstTable().Nodes[0]; got.Problem != burst.ReportProblemUnreachable {
		t.Errorf("row problem = %q, want the mark kept through the requeue tick", got.Problem)
	}
}
