package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// saveLostForward stores a head binding whose round sits on a node under the
// named phase, keeping its queue place in QueuedSince. The prompt log entry
// makes the round open, so the round state the head reports after a requeue
// is the one a served client reads.
func saveLostForward(t *testing.T, env *testEnv, name, node, phase string, round int, since time.Time) {
	t.Helper()
	cwd := filepath.Join(t.TempDir(), name+"-wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	rt := env.runtime(t)
	b := store.Binding{
		Name: name, CWD: cwd, State: store.StateActive, Round: round,
		BuilderCandidate: "claude/anthropic/haiku",
		Burst: &store.BurstFacts{Forward: &store.Forward{
			Node: node, NodeBinding: "deadbeef-" + name, Round: round,
			Phase: phase, ClaimedAt: since.Add(time.Minute), QueuedSince: since,
		}},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	if err := rt.Store.AppendLog(name, store.LogEntry{
		TS: since, Round: round, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("append prompt %s: %v", name, err)
	}
}

// saveStaleForward stores a head binding that carries a forward of an older
// round: the round moved past it, so no requeue may touch it.
func saveStaleForward(t *testing.T, env *testEnv, name, node string, staleRound int, curRound int, since time.Time) {
	t.Helper()
	cwd := filepath.Join(t.TempDir(), name+"-wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	rt := env.runtime(t)
	b := store.Binding{
		Name: name, CWD: cwd, State: store.StateActive, Round: curRound,
		BuilderCandidate: "claude/anthropic/haiku",
		Burst: &store.BurstFacts{Forward: &store.Forward{
			Node: node, NodeBinding: "deadbeef-" + name, Round: staleRound,
			Phase: "sending", ClaimedAt: since, QueuedSince: since,
		}},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// burstBoundEnv is the burst hook env with serve.burst.node_lost_after_ms set
// to lostAfterMS, the way a head configures the loss bound.
func burstBoundEnv(t *testing.T, clock *time.Time, provider burst.Provider, maxBuilders, lostAfterMS int) *testEnv {
	t.Helper()
	ms := lostAfterMS
	return setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = []byte("pem-client-key")
		cfg.Provider = provider
		cfg.MaxBuilders = maxBuilders
		cfg.Now = func() time.Time { return *clock }
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{
			Burst: &policy.BurstPolicy{NodeLostAfterMS: &ms},
		}}
	})
}

// nodeLostEvents returns the burst_node_lost events recorded so far.
func nodeLostEvents(c *countingHooks) []hooks.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []hooks.Event
	for _, e := range c.events {
		if e.Type == hooks.EventBurstNodeLost {
			out = append(out, e)
		}
	}
	return out
}

// TestBurstRequeueLostRestoresFIFO pins the requeue: a forward on a gone node
// or on a node unreachable past the bound returns to the queue at its
// original place and is placeable again, while an unreachable node that has
// not yet outlasted the bound keeps its forward.
func TestBurstRequeueLostRestoresFIFO(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-time.Minute)

	saveForward := func(t *testing.T, rt relevo.Runtime, name, node string) {
		t.Helper()
		saveBurstBinding(t, rt, "a", time.Time{}, 4242)
		cwd := filepath.Join(t.TempDir(), name+"-wt")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		b := store.Binding{
			Name: name, CWD: cwd, State: store.StateActive, Round: 1,
			BuilderCandidate: "claude/anthropic/haiku",
			Burst: &store.BurstFacts{Forward: &store.Forward{
				Node: node, NodeBinding: "deadbeef-" + name, Round: 1,
				Phase: "sending", ClaimedAt: clock.Add(-2 * time.Minute), QueuedSince: since,
			}},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}

	t.Run("gone node returns to its FIFO place and places again", func(t *testing.T) {
		fake := &recordingBurstProvider{}
		env := burstHookEnv(t, &clock, fake, 1)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{burstEligibleRow("b2", 1)}}
		rt := env.runtime(t)
		saveForward(t, rt, "fg", "b9")

		env.srv.requeueLost(context.Background(), clock)
		back := loadBurstBinding(t, rt, "fg")
		if burstStoredForward(back) != nil {
			t.Fatalf("fg forward = %+v, want it cleared", back.Burst.Forward)
		}
		if !back.QueuedAt.Equal(since) {
			t.Fatalf("fg QueuedAt = %v, want the original %v", back.QueuedAt, since)
		}
		if got := env.srv.placeForwards(clock); got != 1 {
			t.Fatalf("placed after requeue = %d, want 1", got)
		}
		again := loadBurstBinding(t, rt, "fg")
		if fwd := burstStoredForward(again); fwd == nil || fwd.Node != "b2" {
			t.Fatalf("fg forward after replace = %+v, want a claim on b2", again.Burst)
		}
	})

	t.Run("row marked gone requeues", func(t *testing.T) {
		fake := &recordingBurstProvider{}
		env := burstHookEnv(t, &clock, fake, 1)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{{ID: "b9", Gone: true}}}
		rt := env.runtime(t)
		saveForward(t, rt, "fg", "b9")

		env.srv.requeueLost(context.Background(), clock)
		back := loadBurstBinding(t, rt, "fg")
		if burstStoredForward(back) != nil || !back.QueuedAt.Equal(since) {
			t.Errorf("fg = forward %+v queuedAt %v, want cleared and %v",
				back.Burst, back.QueuedAt, since)
		}
	})

	t.Run("unreachable past the bound requeues", func(t *testing.T) {
		fake := &recordingBurstProvider{}
		env := burstHookEnv(t, &clock, fake, 1)
		row := burstEligibleRow("b1", 1)
		row.Problem = burst.ReportProblemUnreachable
		row.ProblemSince = clock.Add(-11 * time.Minute)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
		rt := env.runtime(t)
		saveForward(t, rt, "fo", "b1")

		env.srv.requeueLost(context.Background(), clock)
		back := loadBurstBinding(t, rt, "fo")
		if burstStoredForward(back) != nil || !back.QueuedAt.Equal(since) {
			t.Errorf("fo = forward %+v queuedAt %v, want cleared and %v",
				back.Burst, back.QueuedAt, since)
		}
	})

	t.Run("unreachable inside the bound keeps its forward", func(t *testing.T) {
		fake := &recordingBurstProvider{}
		env := burstHookEnv(t, &clock, fake, 1)
		row := burstEligibleRow("b1", 1)
		row.Problem = burst.ReportProblemUnreachable
		row.ProblemSince = clock.Add(-9 * time.Minute)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
		rt := env.runtime(t)
		saveForward(t, rt, "fr", "b1")

		env.srv.requeueLost(context.Background(), clock)
		kept := loadBurstBinding(t, rt, "fr")
		if fwd := burstStoredForward(kept); fwd == nil || fwd.Node != "b1" {
			t.Errorf("fr forward = %+v, want the claim on b1 kept", kept.Burst)
		}
		if !kept.QueuedAt.IsZero() {
			t.Errorf("fr QueuedAt = %v, want zero while the forward is out", kept.QueuedAt)
		}
	})
}

// TestBurstRequeueLostOrdersRequeuedRoundAheadOfLaterQueued pins the FIFO
// place itself, not only the stamp: a round a gone node gives back comes
// back ahead of a round queued after the claim, and with one free node seat
// the next placement hands that seat to the returned round.
func TestBurstRequeueLostOrdersRequeuedRoundAheadOfLaterQueued(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-10 * time.Minute)
	later := clock.Add(-time.Minute)
	fake := &recordingBurstProvider{}
	env := burstHookEnv(t, &clock, fake, 1)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{burstEligibleRow("b2", 1)}}
	rt := env.runtime(t)
	saveBurstBinding(t, rt, "run", time.Time{}, 4242)
	saveLostForward(t, env, "old", "b9", "sending", 1, since)
	saveBurstBinding(t, rt, "new", later, 0)

	env.srv.requeueLost(context.Background(), clock)

	back := loadBurstBinding(t, rt, "old")
	if burstStoredForward(back) != nil {
		t.Fatalf("old forward = %+v, want it cleared", back.Burst.Forward)
	}
	if !back.QueuedAt.Equal(since) {
		t.Fatalf("old QueuedAt = %v, want the original %v", back.QueuedAt, since)
	}
	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(c.Queued) != 2 || c.Queued[0].Name != "old" || c.Queued[1].Name != "new" {
		t.Fatalf("queued fifo = %+v, want old ahead of new", c.Queued)
	}
	if got := env.srv.placeForwards(clock); got != 1 {
		t.Fatalf("placed = %d, want 1", got)
	}
	placed := loadBurstBinding(t, rt, "old")
	if fwd := burstStoredForward(placed); fwd == nil || fwd.Node != "b2" {
		t.Errorf("old forward after placement = %+v, want the freed seat on b2", placed.Burst)
	}
	if after := loadBurstBinding(t, rt, "new"); burstStoredForward(after) != nil || !after.QueuedAt.Equal(later) {
		t.Errorf("new = forward %+v queuedAt %v, want it still queued behind", after.Burst.Forward, after.QueuedAt)
	}
}

// TestBurstRequeueLostHonoursConfiguredBound pins that the bound comes from
// serve.burst.node_lost_after_ms, not the 10-minute default: at a 60-second
// bound a verified node the send path marked unreachable two minutes ago
// loses its round, while one marked thirty seconds ago keeps it. The default
// would requeue neither.
func TestBurstRequeueLostHonoursConfiguredBound(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-3 * time.Minute)

	t.Run("past the configured bound requeues", func(t *testing.T) {
		fake := &recordingBurstProvider{}
		env := burstBoundEnv(t, &clock, fake, 1, 60_000)
		row := burstEligibleRow("b1", 1)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
		saveLostForward(t, env, "fg", "b1", "sending", 1, since)
		env.srv.noteNodeUnreachable(clock.Add(-2*time.Minute), row)

		env.srv.requeueLost(context.Background(), clock)

		back := loadBurstBinding(t, env.runtime(t), "fg")
		if burstStoredForward(back) != nil || !back.QueuedAt.Equal(since) {
			t.Errorf("fg = forward %+v queuedAt %v, want cleared and %v", back.Burst, back.QueuedAt, since)
		}
	})

	t.Run("inside the configured bound keeps its forward", func(t *testing.T) {
		fake := &recordingBurstProvider{}
		env := burstBoundEnv(t, &clock, fake, 1, 60_000)
		row := burstEligibleRow("b1", 1)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
		saveLostForward(t, env, "fo", "b1", "sending", 1, since)
		env.srv.noteNodeUnreachable(clock.Add(-30*time.Second), row)

		env.srv.requeueLost(context.Background(), clock)

		kept := loadBurstBinding(t, env.runtime(t), "fo")
		if fwd := burstStoredForward(kept); fwd == nil || fwd.Node != "b1" {
			t.Errorf("fo forward = %+v, want the claim on b1 kept", kept.Burst)
		}
		if !kept.QueuedAt.IsZero() {
			t.Errorf("fo QueuedAt = %v, want zero while the forward is out", kept.QueuedAt)
		}
	})
}

// TestBurstRequeueLostStopsUnackedCount pins the never-acked close. The head
// closed the round from the node's result and moved to the next round, but
// the node never took the ack, so the forward counted unacked in every
// report. Once the node has stayed unreachable past the bound it is lost:
// the forward clears, the next report counts nothing, and the finished
// round is not queued to run again.
func TestBurstRequeueLostStopsUnackedCount(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-time.Hour)
	fake := &recordingBurstProvider{}
	env := burstHookEnv(t, &clock, fake, 1)
	row := burstEligibleRow("b1", 1)
	row.Problem = burst.ReportProblemUnreachable
	row.ProblemSince = clock.Add(-10 * time.Minute)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
	saveLostForward(t, env, "fq", "b1", "closed", 1, since)
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "fq")
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	before := env.srv.burstForwards()
	if len(before) != 1 || before[0].State != burst.ForwardUnacked || before[0].NodeID != "b1" {
		t.Fatalf("forwards before = %+v, want one unacked on b1", before)
	}

	env.srv.requeueLost(context.Background(), clock)

	if got := env.srv.burstForwards(); len(got) != 0 {
		t.Errorf("forwards after = %+v, want none counted unacked", got)
	}
	back := loadBurstBinding(t, env.runtime(t), "fq")
	if burstStoredForward(back) != nil {
		t.Fatalf("fq forward = %+v, want cleared", back.Burst)
	}
	if !back.QueuedAt.IsZero() || back.Round != 2 {
		t.Fatalf("fq = round %d queuedAt %v, want round 2 not queued: the closed round must not run again", back.Round, back.QueuedAt)
	}
}

// TestBurstNodeLostHookFiresOncePerLostNode pins the hook: one
// burst_node_lost per lost node naming the rounds it gave back, nothing for
// a live node or a round the forward no longer matches, and nothing from a
// second pass over the same episode -- the cleared forward ends it.
func TestBurstNodeLostHookFiresOncePerLostNode(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-time.Minute)
	hookEvents := &countingHooks{}
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := burstVerifyEnv(t, headKP, hookEvents, nil)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
		burstEligibleRow("b1", 1),
		{ID: "b8", Gone: true},
	}}
	rt := env.runtime(t)
	saveLostForward(t, env, "a", "b9", "sending", 1, since)
	saveLostForward(t, env, "b", "b9", "sending", 1, since)
	saveLostForward(t, env, "c", "b8", "sending", 1, since)
	saveLostForward(t, env, "d", "b1", "sending", 1, since)
	saveStaleForward(t, env, "e", "b9", 3, 5, since)

	env.srv.requeueLost(context.Background(), clock)

	evs := nodeLostEvents(hookEvents)
	if len(evs) != 2 {
		t.Fatalf("burst_node_lost events = %d, want one per lost node", len(evs))
	}
	byNode := map[string]string{}
	for _, e := range evs {
		if !e.Timestamp.Equal(clock) {
			t.Errorf("event ts = %v, want %v", e.Timestamp, clock)
		}
		names := strings.Split(e.State, ",")
		sort.Strings(names)
		byNode[e.BindingID] = strings.Join(names, ",")
	}
	if byNode["b9"] != "a,b" {
		t.Errorf("b9 event = %q, want it to name a and b only", byNode["b9"])
	}
	if byNode["b8"] != "c" {
		t.Errorf("b8 event = %q, want it to name c", byNode["b8"])
	}
	if kept := loadBurstBinding(t, rt, "d"); burstStoredForward(kept) == nil {
		t.Errorf("d forward = nil, want the live b1 to keep its round")
	}
	if stale := loadBurstBinding(t, rt, "e"); burstStoredForward(stale) == nil || !stale.QueuedAt.IsZero() {
		t.Errorf("e = forward %+v queuedAt %v, want the stale forward left untouched", stale.Burst.Forward, stale.QueuedAt)
	}
	if got := hookEvents.count(hooks.EventBurstSecurity); got != 0 {
		t.Errorf("burst_security dispatches = %d, want none from a requeue", got)
	}

	env.srv.requeueLost(context.Background(), clock)
	if got := len(nodeLostEvents(hookEvents)); got != 2 {
		t.Errorf("burst_node_lost events after a second pass = %d, want still 2", got)
	}
}

// saveNodeOpenRound stores a node-side binding with an open round, the way a
// node that took the send looks when the head asks after a restart.
func saveNodeOpenRound(t *testing.T, nodeSrv *Server, owner remote.ClientID, name string, round int) {
	t.Helper()
	dir, ok := owner.Dir()
	if !ok {
		t.Fatalf("owner dir for %s: malformed client id", owner)
	}
	cwd := filepath.Join(t.TempDir(), name+"-node-wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	st := nodeSrv.ownerStore(filepath.Join(nodeSrv.cfg.Root, "bindings", dir))
	b := store.Binding{
		Name: name, Owner: string(owner), CWD: cwd,
		State: store.StateActive, Round: round,
		BuilderCandidate: "claude/anthropic/haiku",
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("save node binding %s: %v", name, err)
	}
	if err := st.AppendLog(name, store.LogEntry{
		TS: time.Now().UTC(), Round: round,
		Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("append node prompt %s: %v", name, err)
	}
}

// TestBurstAdoptSending pins the restart resolve: a forward the node still
// holds turns running, while a forward the node never took and one naming
// an unknown node return to the local queue at their FIFO place. The node
// is a real serve server over httptest TLS, reached through the production
// client; only the classify half below runs without the network.
func TestBurstAdoptSending(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := burstVerifyEnv(t, headKP, nil, nil)
	nodeSrv, nodeTS, nodeFP := startBurstNode(t, &headKP)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
		burstRowFor("b1", nodeTS.URL, nodeFP),
	}}

	since := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	headID := remote.IDOf(headKP.Public)
	held := burstNodeBinding(headID, "a")
	saveNodeOpenRound(t, nodeSrv, headID, held, 3)
	saveHeadForward(t, env, "a", "b1", held, 3, since)
	saveHeadForward(t, env, "b", "b1", burstNodeBinding(env.id, "b"), 5, since)
	saveHeadForward(t, env, "c", "b9", "deadbeef-c", 7, since)

	env.srv.adoptSending(context.Background())

	rt := env.runtime(t)
	kept := loadBurstBinding(t, rt, "a")
	if fwd := burstStoredForward(kept); fwd == nil || fwd.Phase != "running" || fwd.Node != "b1" || fwd.Round != 3 {
		t.Errorf("a forward = %+v, want the held round confirmed running on b1", kept.Burst)
	}
	if !kept.QueuedAt.IsZero() {
		t.Errorf("a QueuedAt = %v, want zero while the forward is out", kept.QueuedAt)
	}
	for _, name := range []string{"b", "c"} {
		back := loadBurstBinding(t, rt, name)
		if burstStoredForward(back) != nil {
			t.Errorf("%s forward = %+v, want it cleared", name, back.Burst.Forward)
		}
		if !back.QueuedAt.Equal(since) {
			t.Errorf("%s QueuedAt = %v, want the original %v", name, back.QueuedAt, since)
		}
	}
}

// TestBurstAdoptClassify pins the pure decision adopt fetches feed: open at
// the claimed round stays out, everything else -- a refused fetch, a
// missing or different round, a closed one -- waits locally again. No
// network here; the live fetch above owns the wire.
func TestBurstAdoptClassify(t *testing.T) {
	fwd := store.Forward{Node: "b1", Round: 3, Phase: "sending"}
	view := func(round int, state remote.RoundState) remote.BindingView {
		return remote.BindingView{Round: round, RoundState: state}
	}
	cases := []struct {
		name string
		view remote.BindingView
		err  error
		want bool
	}{
		{"running at the round stays out", view(3, remote.RoundRunning), nil, true},
		{"queued at the round stays out", view(3, remote.RoundQueued), nil, true},
		{"different round requeues", view(4, remote.RoundRunning), nil, false},
		{"closed round requeues", view(3, remote.RoundClosed), nil, false},
		{"idle round requeues", view(3, remote.RoundIdle), nil, false},
		{"broken round requeues", view(3, remote.RoundBroken), nil, false},
		{"missing binding requeues", view(0, ""), errors.New("http 404: not_found"), false},
		{"unreachable node requeues", view(0, ""), client.ErrUnreachable, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := burstAdoptRunning(fwd, c.view, c.err); got != c.want {
				t.Errorf("burstAdoptRunning(%+v, %+v, %v) = %v, want %v", fwd, c.view, c.err, got, c.want)
			}
		})
	}
}
