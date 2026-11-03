package serve

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// observeTestSetup forwards one round to a real node and confirms it
// running: the shape observe closes. It returns the head env, the node
// server and runner, the placed forward and the node owner root.
func observeTestSetup(t *testing.T, clock time.Time) (*testEnv, *Server, *scriptRunner, store.Forward, string) {
	t.Helper()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	nodeSrv, nodeTS, nodeFP := startBurstNodeWithRunner(t, &headKP)
	runner, ok := nodeSrv.cfg.Runner.(*scriptRunner)
	if !ok {
		t.Fatalf("node runner = %T, want *scriptRunner", nodeSrv.cfg.Runner)
	}
	row := stubRow(t, "b1", nodeTS.URL, nodeFP)
	fwd := queueForward(t, env, "api", row, clock)
	if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	headID := remote.IDOf(headKP.Public)
	dir, ok := headID.Dir()
	if !ok {
		t.Fatalf("head id dir: malformed")
	}
	nodeRoot := filepath.Join(nodeSrv.cfg.Root, "bindings", dir)
	return env, nodeSrv, runner, fwd, nodeRoot
}

// headOwnerPath returns the head owner's store root for observeWork.
func headOwnerPath(t *testing.T, env *testEnv) string {
	t.Helper()
	root, err := env.srv.ownerRoot(env.id)
	if err != nil {
		t.Fatalf("ownerRoot: %v", err)
	}
	return root
}

// closeNodeRound commits work on the node's worktree and closes its round
// through the node's own tick, the way a builder finishing looks.
func closeNodeRound(t *testing.T, nodeSrv *Server, runner *scriptRunner, nodeRoot, nodeBinding string, round int) string {
	t.Helper()
	ctx := context.Background()
	st := nodeSrv.ownerStore(nodeRoot)
	b, err := st.Load(nodeBinding)
	if err != nil {
		t.Fatalf("load node %s: %v", nodeBinding, err)
	}
	if err := os.WriteFile(filepath.Join(b.Worktree, "result.txt"), []byte("node result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "add", "result.txt")
	runGit(t, b.Worktree, "commit", "-m", "node round result")
	rt := nodeSrv.runtimeAt(nodeRoot)
	if err := os.WriteFile(rt.Store.ReportPath(nodeBinding, round), []byte("# Report\nDone.\n\n```relevo\nstatus: done\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath(nodeBinding, round), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.setAlive(false)
	if err := nodeSrv.Tick(ctx); err != nil {
		t.Fatalf("node tick: %v", err)
	}
	closed, err := st.Load(nodeBinding)
	if err != nil {
		t.Fatalf("reload node %s: %v", nodeBinding, err)
	}
	if closed.Serve == nil || closed.Serve.ClosedRound != round {
		t.Fatalf("node closed = %+v, want ClosedRound %d", closed.Serve, round)
	}
	if closed.Serve.ResultCommit == "" {
		t.Fatalf("node ResultCommit empty after close")
	}
	return closed.Serve.ResultCommit
}

// TestBurstObserveClosesRound pins the closed path: a node round that
// closes lands on the head through the normal served-close facts, the head
// branch holds the node commit, the head round closes with the forward
// cleared and the ack sent, and the next report is due with reason closed.
func TestBurstObserveClosesRound(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, nodeSrv, runner, fwd, nodeRoot := observeTestSetup(t, clock)
	nodeCommit := closeNodeRound(t, nodeSrv, runner, nodeRoot, fwd.NodeBinding, 1)

	fake, ok := env.srv.burstProvider.(*recordingBurstProvider)
	if !ok {
		t.Fatalf("provider = %T, want the recording fake", env.srv.burstProvider)
	}
	// nodeResp keeps the one test node listed the way a provider response
	// would: an empty answer would mark every row gone on the in-tick merge.
	nodeResp := func(url, fp string) burst.ReportResponse {
		return burst.ReportResponse{
			Version: burst.ReportVersion,
			Nodes: []burst.ReportNodeOut{{
				ID: "b1", Name: "b1 cx43@hel1", URL: url,
				Fingerprint: fp, Cap: 2, State: "ready",
			}},
			ReportEvery: "30s",
		}
	}
	tab := env.srv.copyBurstTable().Nodes[0]
	fake.resp = nodeResp(tab.URL, tab.Fingerprint)

	ctx := context.Background()
	// The close rides a whole tick, not observeForwards alone: the tick's
	// send, verify and place passes are inert here (the forward already
	// runs, nothing is queued) and the report the close owes leaves in the
	// same tick.
	env.srv.burstTick(ctx)
	rt := env.runtime(t)
	back, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if back.Serve == nil || back.Serve.ClosedRound != 1 {
		t.Fatalf("head closed = %+v, want ClosedRound 1", back.Serve)
	}
	if back.Round != 2 {
		t.Errorf("head Round = %d, want 2 after the close", back.Round)
	}
	if burstStoredForward(back) != nil {
		t.Errorf("head forward = %+v, want it cleared after the ack", back.Burst.Forward)
	}
	headSHA, ok, err := env.srv.cfg.Git.RefSHA(ctx, back.Serve.BareRepo, "refs/heads/relevo/api")
	if err != nil || !ok || headSHA != nodeCommit {
		t.Errorf("head branch = %q (ok=%v err=%v), want node commit %q", headSHA, ok, err, nodeCommit)
	}
	// The round ran on the node, so the close facts are the node's: its
	// commit is the result, and the head's untouched worktree is not read as
	// uncommitted work undoing it. That stale worktree is gone, for the next
	// served round to check out again from the branch.
	if back.Serve.ResultCommit != nodeCommit || back.Serve.DirtyCommit != "" {
		t.Errorf("head close facts = result %q dirty %q, want result %q and no dirty commit", back.Serve.ResultCommit, back.Serve.DirtyCommit, nodeCommit)
	}
	if _, ok, _ := env.srv.cfg.Git.RefSHA(ctx, back.Serve.BareRepo, "refs/relevo/api/round-1"); ok {
		t.Error("head wrote an uncommitted-work ref for a round the node ran")
	}
	if back.Worktree == "" {
		t.Fatal("head binding has no worktree path to check")
	}
	if _, err := os.Stat(back.Worktree); !os.IsNotExist(err) {
		t.Errorf("head worktree %s still present (err=%v), want it removed", back.Worktree, err)
	}
	for _, kind := range []string{"report", "diff", "stream"} {
		var path string
		switch kind {
		case "report":
			path = rt.Store.ReportPath("api", 1)
		case "diff":
			path = rt.Store.DiffPath("api", 1)
		default:
			path = rt.Store.StreamPath("api", 1)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("head %s missing: %v", kind, err)
		}
	}
	nodeSt := nodeSrv.ownerStore(nodeRoot)
	nodeBack, err := nodeSt.Load(fwd.NodeBinding)
	if err != nil {
		t.Fatal(err)
	}
	if nodeBack.Serve == nil || nodeBack.Serve.AckedRound != 1 {
		t.Errorf("node AckedRound = %+v, want 1: the ack did not land", nodeBack.Serve)
	}
	// The close makes a report due at once, out of cycle, with reason
	// closed: reportCycle prefers closed over forwarded and startup.
	if got := fake.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want the close to report in its tick", got)
	}
	if got := fake.last().Reason; got != burst.ReportReasonClosed {
		t.Errorf("reason = %q, want closed for the closed round", got)
	}
}

// TestBurstObserveCachesNodeLive pins the live half: a running node round's
// view is cached, and ServedLive serves it for the forwarded binding
// without dialling the node again.
func TestBurstObserveCachesNodeLive(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, _, _, fwd, _ := observeTestSetup(t, clock)
	_ = fwd
	ctx := context.Background()
	if got := env.srv.observeForwards(ctx); got != 0 {
		t.Fatalf("observe closed = %d, want 0 for a running round", got)
	}
	rt := env.runtime(t)
	back, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	got, err := relevo.ServedLive(ctx, rt, back)
	if err != nil {
		t.Fatalf("ServedLive: %v", err)
	}
	if got == nil {
		t.Fatalf("ServedLive = nil, want the cached node view")
	}
}

// TestBurstObserveStaleLiveNotCached pins the cache gate: a node answering
// a round other than its forward's has no live cached, so the head round's
// ServedLive falls through to the local row instead of serving the node's
// other round as the head's work.
func TestBurstObserveStaleLiveNotCached(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	script := &nodeScript{
		getStatus: http.StatusOK,
		getView: remote.BindingView{
			Name: "nb-api", Round: 2, RoundState: remote.RoundRunning,
			Live: &remote.LiveView{Tail: []string{"node-round-2-marker"}},
		},
	}
	url, fp := startStubNode(t, script.serve())
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{stubRow(t, "b1", url, fp)}}
	since := clock.Add(-time.Minute)
	saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Burst.Forward.NodeBinding = "nb-api"
	b.Burst.Forward.Phase = "running"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.observeForwards(context.Background()); got != 0 {
		t.Fatalf("observe closed = %d, want 0 for an open round", got)
	}
	back, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	got, err := relevo.ServedLive(context.Background(), rt, back)
	if err != nil {
		t.Fatalf("ServedLive: %v", err)
	}
	if got == nil {
		t.Fatal("ServedLive = nil, want the local row")
	}
	if strings.Contains(strings.Join(got.Tail, "\n"), "node-round-2-marker") {
		t.Errorf("ServedLive tail = %q, want none of the stale round's view", strings.Join(got.Tail, " | "))
	}
}

// TestBurstObserveStaleApplyDropped pins the apply-time match: a fetch for a
// forward the head already moved past applies nothing. Dropping the match
// check would close the superseded round.
func TestBurstObserveStaleApplyDropped(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	row := stubRow(t, "b1", closedPortURL(t), burstTestFingerprint)
	fwd := queueForward(t, env, "api", row, clock)
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("advance round: %v", err)
	}
	fetch := &observeFetch{
		view:   stubRoundView(fwd.NodeBinding, 1, remote.RoundClosed),
		report: []byte("# Report\nDone.\n"),
	}
	if env.srv.applyObserveClosed(context.Background(), observeWork{ownerPath: headOwnerPath(t, env), name: "api", fwd: fwd}, fetch, clock) {
		t.Fatalf("stale apply applied, want it dropped")
	}
	back, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if back.Serve != nil && back.Serve.ClosedRound == 1 {
		t.Errorf("head closed = %+v, want no close for a superseded forward", back.Serve)
	}
	if have := burstStoredForward(back); have == nil {
		t.Errorf("head forward cleared, want the superseded claim kept")
	}
}

// TestBurstObserveNeedsYou pins the waiting path: a node round that waits
// on its human, or whose builder is gone, parks the head binding in NEEDS
// YOU with the node's reason and report, and clears the forward. A parked
// round answers none of the head's work, so both states need the human.
func TestBurstObserveNeedsYou(t *testing.T) {
	for _, state := range []remote.RoundState{remote.RoundNeedsYou, remote.RoundBroken} {
		t.Run(string(state), func(t *testing.T) {
			clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
			headKP, err := remote.Generate()
			if err != nil {
				t.Fatal(err)
			}
			env := forwardEnv(t, headKP)
			script := &nodeScript{
				getStatus: http.StatusOK,
				getView: remote.BindingView{
					Name: "nb-api", Round: 1, RoundState: state,
					Halt: "node needs a human: bad base",
				},
			}
			url, fp := startStubNode(t, script.serve())
			env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{stubRow(t, "b1", url, fp)}}
			since := clock.Add(-time.Minute)
			saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
			rt := env.runtime(t)
			b, err := rt.Store.Load("api")
			if err != nil {
				t.Fatal(err)
			}
			b.Burst.Forward.NodeBinding = "nb-api"
			b.Burst.Forward.Phase = "running"
			if b.Burst.Shipped == nil {
				b.Burst.Shipped = map[string]string{}
			}
			b.Burst.Shipped["b1"] = strings.Repeat("a", 40)
			b.Burst.NodeBindings = append(b.Burst.NodeBindings, "nb-api")
			if err := rt.Store.Save(b); err != nil {
				t.Fatal(err)
			}
			if got := env.srv.observeForwards(context.Background()); got != 0 {
				t.Fatalf("observe closed = %d, want 0 for a waiting round", got)
			}
			back, err := rt.Store.Load("api")
			if err != nil {
				t.Fatal(err)
			}
			if back.State != store.StateNeedsYou {
				t.Errorf("head state = %q, want needs_you", back.State)
			}
			if !strings.Contains(back.Halt, "node needs a human") {
				t.Errorf("head halt = %q, want the node's reason", back.Halt)
			}
			parked, err := os.ReadFile(rt.Store.ReportPath("api", 1))
			if err != nil {
				t.Errorf("head report not parked: %v", err)
			} else if !strings.Contains(string(parked), "node needs a human") {
				t.Errorf("head report = %q, want the node's view carrying its halt", parked)
			}
			if burstStoredForward(back) != nil {
				t.Errorf("head forward = %+v, want it cleared", back.Burst.Forward)
			}
		})
	}
}

// TestBurstObserveAckFailureKeepsClosed pins the ack retry: a failed ack
// keeps Phase closed so the next cycle retries, and the next ack clears the
// forward.
func TestBurstObserveAckFailureKeepsClosed(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	failHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, remote.ErrorBody{Code: remote.Code("internal"), Message: "boom"})
	})
	failURL, failFP := startStubNode(t, failHandler)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{stubRow(t, "b1", failURL, failFP)}}
	since := clock.Add(-time.Minute)
	saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Round = 2
	if b.Serve == nil {
		b.Serve = &store.ServeFacts{}
	}
	b.Serve.ClosedRound = 1
	b.Burst.Forward.Phase = "closed"
	b.Burst.Forward.NodeBinding = "nb-api"
	// The shape a confirmed forward carries (see confirmSend): while ship
	// facts stand, the empty-burst wipe never empties the whole struct, so
	// the retry's clear can only come from the Forward write itself.
	if b.Burst.Shipped == nil {
		b.Burst.Shipped = map[string]string{}
	}
	b.Burst.Shipped["b1"] = strings.Repeat("a", 40)
	b.Burst.NodeBindings = append(b.Burst.NodeBindings, "nb-api")
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	w := observeWork{ownerPath: headOwnerPath(t, env), name: "api", fwd: *burstStoredForward(b)}
	failCl, err := env.srv.burstNodeClient([]burst.NodeRow{stubRow(t, "b1", failURL, failFP)})
	if err != nil {
		t.Fatal(err)
	}
	if env.srv.ackObserveClosed(context.Background(), failCl, w) {
		t.Fatalf("failing ack cleared, want Phase closed kept")
	}
	kept, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if have := burstStoredForward(kept); have == nil || have.Phase != "closed" {
		t.Fatalf("head forward = %+v, want Phase closed kept for retry", kept.Burst)
	}
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, stubRoundView("nb-api", 1, remote.RoundClosed))
	})
	okURL, okFP := startStubNode(t, okHandler)
	okCl, err := env.srv.burstNodeClient([]burst.NodeRow{stubRow(t, "b1", okURL, okFP)})
	if err != nil {
		t.Fatal(err)
	}
	if !env.srv.ackObserveClosed(context.Background(), okCl, w) {
		t.Fatalf("retry ack did not clear, want the forward gone")
	}
	cleared, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if burstStoredForward(cleared) != nil {
		t.Errorf("head forward = %+v, want it cleared after the ack", cleared.Burst.Forward)
	}
}

// TestBurstObserveUnreachableMarked pins the reachability half: a node down
// during observe marks the row unreachable, and a success clears it.
func TestBurstObserveUnreachableMarked(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	row := stubRow(t, "b1", closedPortURL(t), burstTestFingerprint)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
	since := clock.Add(-time.Minute)
	saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Burst.Forward.NodeBinding = "nb-api"
	b.Burst.Forward.Phase = "running"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.observeForwards(context.Background()); got != 0 {
		t.Fatalf("observe closed = %d, want 0 when the node is down", got)
	}
	if got := env.srv.copyBurstTable().Nodes[0].Problem; got != burst.ReportProblemUnreachable {
		t.Errorf("Problem = %q, want unreachable while the node is down", got)
	}
	env.srv.clearNodeProblem(row)
	if got := env.srv.copyBurstTable().Nodes[0].Problem; got != "" {
		t.Errorf("Problem = %q, want a success to clear it", got)
	}
}

// TestBurstClosedForwardLeavesNextRoundAdmissible pins the MasterMind
// amendment: a closed forward still waiting for its ack never blocks the
// binding's next round. The next queued round admits locally, and placement
// refuses to forward it until the ack clears the forward.
func TestBurstClosedForwardLeavesNextRoundAdmissible(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	row := stubRow(t, "b1", closedPortURL(t), burstTestFingerprint)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
	since := clock.Add(-time.Minute)
	saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Round = 2
	if b.Serve == nil {
		b.Serve = &store.ServeFacts{}
	}
	b.Serve.ClosedRound = 1
	b.Burst.Forward.Phase = "closed"
	b.Burst.Forward.NodeBinding = "nb-api"
	b.QueuedAt = clock
	b.State = store.StateActive
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.placeForwards(clock); got != 0 {
		t.Errorf("placed = %d, want 0: a closed forward is never overwritten", got)
	}
	after, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if have := burstStoredForward(after); have == nil || have.Phase != "closed" {
		t.Fatalf("forward = %+v, want the closed forward kept", after.Burst)
	}
	entries, _ := rt.Store.ReadLog("api")
	if got := relevo.ServedView(after, entries, "", "").Node; got != "" {
		t.Errorf("ServedView.Node = %q, want empty while the forward is closed", got)
	}
	if err := env.srv.admitLocked(context.Background()); err != nil {
		t.Fatalf("admit: %v", err)
	}
	admitted, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Builder.PID == 0 {
		t.Errorf("next round not admitted locally, want a local process despite the unacked forward")
	}
	if have := burstStoredForward(admitted); have == nil || have.Phase != "closed" {
		t.Errorf("forward = %+v, want the closed forward kept across the local admit", admitted.Burst)
	}
}

// TestBurstAckStuckFiresOncePerEpisode pins the ack episode: three
// consecutive refused acks on a reachable node fire one burst_ack_stuck,
// a fourth failure fires nothing, every failure is logged, and the node
// keeps the closed forward and keeps being reported unacked -- a refused
// receipt never marks the node unreachable and never requeues. An ack that
// lands ends the episode, and so does a forward that leaves while the
// observe pass runs without it: either way a fresh streak of three fires
// afresh.
func TestBurstAckStuckFiresOncePerEpisode(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	hookEvents := &countingHooks{}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
		cfg.Hooks = hookEvents
	})
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-time.Minute)
	failURL, failFP := startStubNode(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, remote.ErrorBody{Code: remote.Code("internal"), Message: "boom"})
	}))
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{stubRow(t, "b1", failURL, failFP)}}
	saveLostForward(t, env, "fq", "b1", "closed", 1, since)
	rt := env.runtime(t)
	w := observeWork{ownerPath: headOwnerPath(t, env), name: "fq", fwd: *burstStoredForward(loadBurstBinding(t, rt, "fq"))}
	failCl, err := env.srv.burstNodeClient([]burst.NodeRow{stubRow(t, "b1", failURL, failFP)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if env.srv.ackObserveClosed(ctx, failCl, w) {
		t.Fatal("failing ack 1 cleared the forward")
	}
	if env.srv.ackObserveClosed(ctx, failCl, w) {
		t.Fatal("failing ack 2 cleared the forward")
	}
	// b1 still holds the closed forward, so the observe pass's forget must
	// keep its standing count: only an arm that checks the holding set can
	// tell this from a forward that left, and the streak has to survive.
	env.srv.burstHealth.forgetAckEpisodes(map[string]bool{"b1": true})
	if env.srv.ackObserveClosed(ctx, failCl, w) {
		t.Fatal("failing ack 3 cleared the forward")
	}
	if got := hookEvents.count(hooks.EventBurstAckStuck); got != 1 {
		t.Fatalf("burst_ack_stuck = %d, want exactly one after three failing cycles", got)
	}
	if got := strings.Count(logs.String(), "observe ack failed"); got != 3 {
		t.Errorf("ack-failure log lines = %d, want each failure logged", got)
	}
	if kept := burstStoredForward(loadBurstBinding(t, rt, "fq")); kept == nil || kept.Phase != "closed" {
		t.Fatalf("fq forward = %+v, want the closed claim kept through the failures", kept)
	}
	if got := env.srv.burstForwards(); len(got) != 1 || got[0].State != burst.ForwardUnacked || got[0].NodeID != "b1" {
		t.Errorf("forwards = %+v, want one unacked on b1", got)
	}
	if row := env.srv.copyBurstTable().Nodes[0]; row.Problem != "" {
		t.Errorf("row Problem = %q, want an ack failure never marking the node unreachable", row.Problem)
	}

	if env.srv.ackObserveClosed(ctx, failCl, w) {
		t.Fatal("fourth failing ack cleared the forward")
	}
	if got := hookEvents.count(hooks.EventBurstAckStuck); got != 1 {
		t.Errorf("burst_ack_stuck after a fourth failure = %d, want still 1: once per episode", got)
	}

	okURL, okFP := startStubNode(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, stubRoundView("deadbeef-fq", 1, remote.RoundClosed))
	}))
	okCl, err := env.srv.burstNodeClient([]burst.NodeRow{stubRow(t, "b1", okURL, okFP)})
	if err != nil {
		t.Fatal(err)
	}
	if !env.srv.ackObserveClosed(ctx, okCl, w) {
		t.Fatal("successful ack did not clear the forward")
	}
	if burstStoredForward(loadBurstBinding(t, rt, "fq")) != nil {
		t.Fatal("fq forward survives the successful ack, want it cleared")
	}
	for i := 0; i < 3; i++ {
		env.srv.ackObserveClosed(ctx, failCl, w)
	}
	if got := hookEvents.count(hooks.EventBurstAckStuck); got != 2 {
		t.Fatalf("burst_ack_stuck in a second episode = %d, want 2: the success reset the count", got)
	}

	// The forward left without another ack ever landing: the observe pass
	// sees no closed forward on b1 and forgets the standing count, so a
	// third streak fires as a new episode.
	env.srv.observeForwards(ctx)
	for i := 0; i < 3; i++ {
		env.srv.ackObserveClosed(ctx, failCl, w)
	}
	if got := hookEvents.count(hooks.EventBurstAckStuck); got != 3 {
		t.Errorf("burst_ack_stuck after the forward left = %d, want 3: leaving ends the episode too", got)
	}
}

// TestBurstObserveKeepsNodeUncommittedWork pins the other close fact: work
// the node's builder left uncommitted reaches the head as the round's
// uncommitted-work commit, the node's own snapshot, not one taken from the
// head worktree.
func TestBurstObserveKeepsNodeUncommittedWork(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, nodeSrv, runner, fwd, nodeRoot := observeTestSetup(t, clock)
	nb, err := nodeSrv.ownerStore(nodeRoot).Load(fwd.NodeBinding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nb.Worktree, "leftover.txt"), []byte("not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	closeNodeRound(t, nodeSrv, runner, nodeRoot, fwd.NodeBinding, 1)

	fake, ok := env.srv.burstProvider.(*recordingBurstProvider)
	if !ok {
		t.Fatalf("provider = %T, want the recording fake", env.srv.burstProvider)
	}
	tab := env.srv.copyBurstTable().Nodes[0]
	fake.resp = burst.ReportResponse{
		Version: burst.ReportVersion,
		Nodes: []burst.ReportNodeOut{{
			ID: "b1", Name: "b1", URL: tab.URL, Fingerprint: tab.Fingerprint, Cap: 2, State: "ready",
		}},
		ReportEvery: "30s",
	}
	ctx := context.Background()
	env.srv.burstTick(ctx)

	back, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if back.Serve == nil || back.Serve.ClosedRound != 1 || back.Serve.DirtyCommit == "" {
		t.Fatalf("head close = %+v, want round 1 closed with the node's uncommitted work", back.Serve)
	}
	side, ok, err := env.srv.cfg.Git.RefSHA(ctx, back.Serve.BareRepo, "refs/relevo/api/round-1")
	if err != nil || !ok || side != back.Serve.DirtyCommit {
		t.Errorf("head side ref = %q (ok=%v err=%v), want the dirty commit %q", side, ok, err, back.Serve.DirtyCommit)
	}
	if _, ok, err := env.srv.cfg.Git.BlobAt(ctx, back.Serve.BareRepo, back.Serve.DirtyCommit, "leftover.txt"); err != nil || !ok {
		t.Errorf("dirty commit lacks the node's leftover.txt (ok=%v err=%v)", ok, err)
	}
}
