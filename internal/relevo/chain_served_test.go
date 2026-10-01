package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// servedChainRuntime is a runtime a served chain can be created on: the chain
// runtime's fake git and runner, with the chain roles file.
func servedChainRuntime(t *testing.T) (Runtime, *fakeGit, *fakeRunner) {
	t.Helper()
	rt, fg := chainRuntime(t)
	return rt, fg, rt.Runner.(*fakeRunner)
}

// servedChainRequest is the wire request a server's HTTP layer would build for
// name: one plan, the default actors and no security phase.
func servedChainRequest(name string) ServedChainRequest {
	return ServedChainRequest{
		Name:        name,
		Plans:       []string{"build it"},
		Settings:    remote.ChainSettings{MaxCorrections: 2, ReviewerActor: "reviewer", PlannerActor: "lite-planner"},
		Feature:     "auth",
		Owner:       "alice",
		Bare:        "/bare/" + name + ".git",
		RepoID:      strings.Repeat("a", 64),
		Base:        "commit-head-123",
		AuthorName:  "Test User",
		AuthorEmail: "test@example.com",
	}
}

// servedChainStart preflights and creates one served chain, returning what the
// create produced.
func servedChainStart(t *testing.T, rt Runtime, req ServedChainRequest) ChainResult {
	t.Helper()
	plan, err := ServedChainPreflight(rt, req)
	if err != nil {
		t.Fatalf("ServedChainPreflight: %v", err)
	}
	res, err := ServedChainCreate(context.Background(), rt, plan, rt.Store.WorktreePath(req.Name))
	if err != nil {
		t.Fatalf("ServedChainCreate: %v", err)
	}
	return res
}

// assertServedChainAbsent asserts a refused served preflight or create left no
// chain row and no member binding.
func assertServedChainAbsent(t *testing.T, rt Runtime, name string) {
	t.Helper()
	if _, err := rt.Store.Chain(name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("chain %q exists after a refusal: %v", name, err)
	}
	for _, member := range []string{name, name + "-rev", name + "-plan", name + "-sec"} {
		if _, err := rt.Store.Load(member); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding %q exists after a refusal: %v", member, err)
		}
	}
}

// TestServedChainPreflightRefusesBeforeAnythingExists pins the read-only
// preflight: a taken member name, a builder actor the registry calls a reader,
// and an unknown actor each refuse with nothing created.
func TestServedChainPreflightRefusesBeforeAnythingExists(t *testing.T) {
	t.Parallel()

	t.Run("a taken member name", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		if err := rt.Store.Save(store.Binding{Name: "shop-rev", CWD: "/repo", Round: 1}); err != nil {
			t.Fatalf("seed the taken member: %v", err)
		}
		_, err := ServedChainPreflight(rt, servedChainRequest("shop"))
		if err == nil || !strings.Contains(err.Error(), "shop-rev") {
			t.Fatalf("err = %v, want the taken-member refusal", err)
		}
		if _, err := rt.Store.Chain("shop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("chain exists after the refusal: %v", err)
		}
	})

	t.Run("a reader actor in the builder part", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		req := servedChainRequest("shop")
		req.BuilderActor = "reviewer"

		_, err := ServedChainPreflight(rt, req)
		if err == nil || !strings.Contains(err.Error(), `actor "reviewer" must be a writer actor, not a reader`) {
			t.Fatalf("err = %v, want the shape refusal", err)
		}
		assertServedChainAbsent(t, rt, "shop")
	})

	t.Run("an unknown actor", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		req := servedChainRequest("shop")
		req.Settings.ReviewerActor = "ghost"

		_, err := ServedChainPreflight(rt, req)
		if err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Fatalf("err = %v, want the unknown-actor refusal", err)
		}
		assertServedChainAbsent(t, rt, "shop")
	})
}

// TestServedChainCreateWritesTheChainAndMembersAtomically pins the served
// create: one running row awaiting the builder, every member in the served shape
// with its Serve facts, the plan copies, and plan 1 queued for admit. A create
// whose row is refused leaves no member behind.
func TestServedChainCreateWritesTheChainAndMembersAtomically(t *testing.T) {
	t.Parallel()

	rt, _, fr := servedChainRuntime(t)
	req := servedChainRequest("shop")
	res := servedChainStart(t, rt, req)

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.Phase != string(chain.PhaseBuild) ||
		row.Step != string(chain.StepBuilding) || row.Plan != 1 || row.Plans != 1 ||
		row.AwaitingMember != chain.MemberBuilder || row.AwaitingRound != 1 {
		t.Fatalf("chain row = %+v, want running/build/building plan 1 of 1 awaiting the builder", row)
	}
	if row.Server != "" || row.MasterMindID != "" {
		t.Errorf("row Server/MasterMindID = %q/%q, want both empty", row.Server, row.MasterMindID)
	}
	if row.Branch != "relevo/shop" || row.Worktree != rt.Store.WorktreePath("shop") || row.Repo != req.Bare || row.Base != req.Base {
		t.Errorf("row branch/worktree/repo/base = %q/%q/%q/%q, want the served facts",
			row.Branch, row.Worktree, row.Repo, row.Base)
	}

	if len(res.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(res.Members))
	}
	for _, m := range res.Members {
		if m.Owner != "alice" || m.Serve == nil || m.Serve.BareRepo != req.Bare || m.Serve.RepoID != req.RepoID {
			t.Errorf("member %s = owner %q serve %+v, want alice with the request's Serve facts", m.Name, m.Owner, m.Serve)
		}
	}

	body, err := rt.Store.ReadFile(rt.Store.ChainPlanPath("shop", 1))
	if err != nil || string(body) != "build it" {
		t.Fatalf("plan copy = %q, %v; want \"build it\"", string(body), err)
	}

	builder := chainBinding(t, rt, "shop")
	if builder.QueuedAt.IsZero() {
		t.Error("plan 1 is not queued: QueuedAt is zero")
	}
	if len(fr.specs) != 0 {
		t.Errorf("runner starts = %d, want none: a served round queues", len(fr.specs))
	}
	if !promptNoteFor(chainLog(t, rt, "shop"), 1, "chain builder") {
		t.Errorf("log = %+v, want the plan-1 prompt entry with the chain step note", chainLog(t, rt, "shop"))
	}

	t.Run("a security chain holds four members", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		req := servedChainRequest("shop")
		req.Settings.Security = true
		req.Settings.SecurityActor = "security"
		res := servedChainStart(t, rt, req)
		if len(res.Members) != 4 {
			t.Fatalf("members = %d, want 4 with the security phase on", len(res.Members))
		}
		sec := chainBinding(t, rt, "shop-sec")
		if sec.Owner != "alice" || sec.Shape != store.ShapeReader {
			t.Errorf("security member = owner %q shape %q, want a served reader", sec.Owner, sec.Shape)
		}
	})

	t.Run("a refused row leaves no member", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		req := servedChainRequest("shop")
		plan, err := ServedChainPreflight(rt, req)
		if err != nil {
			t.Fatalf("ServedChainPreflight: %v", err)
		}
		plan.req.Plans = nil
		if _, err := ServedChainCreate(context.Background(), rt, plan, rt.Store.WorktreePath("shop")); err == nil {
			t.Fatal("ServedChainCreate = nil, want the invalid-row refusal")
		}
		assertServedChainAbsent(t, rt, "shop")
	})
}

// TestServedChainReadersShareTheBuilderTree pins the shared-tree rule: every
// reader's CWD is the builder's served worktree, with no worktree and no branch
// of its own.
func TestServedChainReadersShareTheBuilderTree(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))

	builder := chainBinding(t, rt, "shop")
	if builder.Worktree != rt.Store.WorktreePath("shop") || builder.Branch != "relevo/shop" {
		t.Fatalf("builder = worktree %q branch %q, want the served pair", builder.Worktree, builder.Branch)
	}
	for _, name := range []string{"shop-rev", "shop-plan"} {
		m := chainBinding(t, rt, name)
		if m.CWD != builder.Worktree {
			t.Errorf("%s CWD = %q, want the builder's worktree %q", name, m.CWD, builder.Worktree)
		}
		if m.Worktree != "" || m.Branch != "" {
			t.Errorf("%s worktree/branch = %q/%q, want both empty", name, m.Worktree, m.Branch)
		}
	}
}

// TestServedChainMemberSendQueues pins the chain-driven served send: a chain
// round for a served member is queued for admit -- no process starts -- while a
// local member still starts its round.
func TestServedChainMemberSendQueues(t *testing.T) {
	t.Parallel()

	t.Run("a served member queues", func(t *testing.T) {
		t.Parallel()

		rt, _, fr := servedChainRuntime(t)
		servedChainStart(t, rt, servedChainRequest("shop"))

		builder := chainBinding(t, rt, "shop")
		ev := chain.Event{
			Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
			Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen,
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			_, err := chainApply(context.Background(), rt, tx, builder, ev, nil)
			return err
		}); err != nil {
			t.Fatalf("chainApply: %v", err)
		}

		rev := chainBinding(t, rt, "shop-rev")
		if rev.QueuedAt.IsZero() {
			t.Error("the served reviewer's round is not queued: QueuedAt is zero")
		}
		if !rev.RoundStartedAt.IsZero() {
			t.Errorf("RoundStartedAt = %v, want zero for a queued round", rev.RoundStartedAt)
		}
		if len(fr.specs) != 0 {
			t.Errorf("runner starts = %d, want none: a served round queues", len(fr.specs))
		}
		if !promptNoteFor(chainLog(t, rt, "shop-rev"), 1, "chain reviewer") {
			t.Errorf("reviewer log = %+v, want the prompt entry with the chain reviewer note", chainLog(t, rt, "shop-rev"))
		}
	})

	t.Run("a local member still starts", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		fr := rt.Runner.(*fakeRunner)
		before := len(fr.specs)

		chainBuilderClose(t, rt, "shop", chainDoneBody())

		if len(fr.specs) != before+1 {
			t.Errorf("runner starts = %d, want %d: the local reviewer must start", len(fr.specs), before+1)
		}
		if rev := chainBinding(t, rt, "shop-rev"); !rev.QueuedAt.IsZero() {
			t.Errorf("local reviewer QueuedAt = %v, want zero", rev.QueuedAt)
		}
	})
}

// TestCloseServedRoundRecordsABranchlessReader pins the branchless close: a
// reader with no branch records ClosedRound with no git call and no git facts,
// while the writer path is unchanged.
func TestCloseServedRoundRecordsABranchlessReader(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	reader := store.Binding{
		Name: "shop-rev", Owner: "alice", CWD: "/build/shop", Round: 2,
		Shape: store.ShapeReader, Serve: &store.ServeFacts{BareRepo: "/bare/shop.git"},
	}
	got := closeServedRound(context.Background(), rt, reader)
	if got.Serve.ClosedRound != 1 {
		t.Errorf("reader ClosedRound = %d, want 1", got.Serve.ClosedRound)
	}
	if got.Serve.ResultCommit != "" || got.Serve.DirtyCommit != "" {
		t.Errorf("reader result/dirty = %q/%q, want both empty", got.Serve.ResultCommit, got.Serve.DirtyCommit)
	}
	if fg.calls != 0 {
		t.Errorf("git calls = %d, want none for a branchless reader", fg.calls)
	}

	writer := store.Binding{
		Name: "shop", Owner: "alice", CWD: "/build/shop", Worktree: "/build/shop",
		Branch: "relevo/shop", Round: 2, Shape: store.ShapeWriter,
		Serve: &store.ServeFacts{BareRepo: "/bare/shop.git"},
	}
	gotWriter := closeServedRound(context.Background(), rt, writer)
	if gotWriter.Serve.ClosedRound != 1 || gotWriter.Serve.ResultCommit != "fakerefsha" {
		t.Errorf("writer close = closed %d result %q, want 1/fakerefsha", gotWriter.Serve.ClosedRound, gotWriter.Serve.ResultCommit)
	}
	if fg.calls == 0 {
		t.Error("the writer's close must have made a git call")
	}
}

// TestServedChainEndQueuesNoDelivery pins the delivery skip: a served chain
// writes its terminal row and trace but queues no end delivery, while a local
// chain still queues one.
func TestServedChainEndQueuesNoDelivery(t *testing.T) {
	t.Parallel()

	t.Run("a served chain queues nothing", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		servedChainStart(t, rt, servedChainRequest("shop"))

		builder := chainBinding(t, rt, "shop")
		builderEv := chain.Event{
			Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
			Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen,
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			_, err := chainApply(context.Background(), rt, tx, builder, builderEv, nil)
			return err
		}); err != nil {
			t.Fatalf("builder chainApply: %v", err)
		}

		rev := chainBinding(t, rt, "shop-rev")
		revEv := chain.Event{
			Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 1,
			Verdict: chain.VerdictPass,
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			_, err := chainApply(context.Background(), rt, tx, rev, revEv, nil)
			return err
		}); err != nil {
			t.Fatalf("reviewer chainApply: %v", err)
		}

		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
			t.Errorf("chain status = %q, want done", row.Status)
		}
		if got := len(chainTrace(t, rt, "shop")); got != 2 {
			t.Errorf("trace rows = %d, want 2", got)
		}
		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 0 {
			t.Errorf("pending deliveries = %d, want none on a served chain", len(pending))
		}
	})

	t.Run("a local chain still queues one", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
			t.Fatalf("chain status = %q, want done", row.Status)
		}
		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
			t.Errorf("pending deliveries = %d, want the one end delivery", len(pending))
		}
	})
}

// TestServedChainResumeCreatesAServedSecurityMember pins the late security
// member: turning the phase on at resume creates it in the served shape, as a
// reader sharing the builder's tree with no worktree and no branch.
func TestServedChainResumeCreatesAServedSecurityMember(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))

	// Free the builder's round so the resume has a member round to re-send:
	// advance it, the shape a collected close leaves.
	builder := chainBinding(t, rt, "shop")
	builder.Round = 2
	if err := rt.Store.Save(builder); err != nil {
		t.Fatalf("advance the builder: %v", err)
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusHalted)
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("halt the chain: %v", err)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Security: ptr(true)}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	builder = chainBinding(t, rt, "shop")
	sec := chainBinding(t, rt, "shop-sec")
	if sec.Owner != "alice" || sec.Serve == nil {
		t.Errorf("security member = owner %q serve %+v, want a served binding", sec.Owner, sec.Serve)
	}
	if sec.Shape != store.ShapeReader || sec.Worktree != "" || sec.Branch != "" {
		t.Errorf("security member shape/worktree/branch = %q/%q/%q, want a branchless reader", sec.Shape, sec.Worktree, sec.Branch)
	}
	if sec.CWD != builder.Worktree {
		t.Errorf("security member CWD = %q, want the builder's worktree %q", sec.CWD, builder.Worktree)
	}
}

// TestServedRoundFactsAreThatRounds pins the extraction: servedRoundFacts names
// the round it is given -- its outcome, gate, stop, diff and usage -- while
// ServedView still reports the binding's own ClosedRound.
func TestServedRoundFactsAreThatRounds(t *testing.T) {
	t.Parallel()

	entries := []store.LogEntry{
		{
			Round: 1, Kind: store.KindReport, Outcome: "done",
			Gate: &store.GateRecord{Result: "pass"}, Usage: &usage.Usage{Model: "one"}, Rusage: &store.Rusage{CPUMS: 11},
		},
		{Round: 1, Kind: store.KindDiff, Note: "one", Commits: 1, Tree: "tree-1"},
		{Round: 1, Kind: store.KindStop, Note: "stopped/killed"},
		{
			Round: 2, Kind: store.KindReport, Outcome: "halted",
			Gate: &store.GateRecord{Result: "fail"}, Usage: &usage.Usage{Model: "two"}, Rusage: &store.Rusage{CPUMS: 22},
		},
		{Round: 2, Kind: store.KindDiff, Note: "two", Commits: 2, Tree: "tree-2"},
	}

	one := servedRoundFacts(entries, 1)
	if one.Round != 1 || one.ReportOutcome != "done" || one.GateResult != "pass" || one.Stopped != "killed" ||
		one.DiffNote != "one" || one.DiffCommits != 1 || one.DiffTree != "tree-1" ||
		one.Usage == nil || one.Usage.Model != "one" || one.Rusage == nil || one.Rusage.CPUMS != 11 {
		t.Errorf("round 1 facts = %+v, want the round-1 fields", one)
	}

	two := servedRoundFacts(entries, 2)
	if two.Round != 2 || two.ReportOutcome != "halted" || two.GateResult != "fail" || two.Stopped != "" ||
		two.DiffNote != "two" || two.DiffCommits != 2 || two.DiffTree != "tree-2" ||
		two.Usage == nil || two.Usage.Model != "two" || two.Rusage == nil || two.Rusage.CPUMS != 22 {
		t.Errorf("round 2 facts = %+v, want the round-2 fields", two)
	}

	b := store.Binding{
		Name: "shop", Owner: "alice", Round: 2,
		Serve: &store.ServeFacts{ClosedRound: 2, AckedRound: 0},
	}
	view := ServedView(b, entries, "rec-1", "install-1")
	if view.ClosedRound != 2 || view.ReportOutcome != "halted" || view.GateResult != "fail" ||
		view.DiffNote != "two" || view.DiffTree != "tree-2" || view.Stopped != "" ||
		view.Usage == nil || view.Usage.Model != "two" {
		t.Errorf("ServedView = %+v, want it unchanged at ClosedRound 2", view)
	}
}

// TestServedChainViewCarriesRoundsAndTrace pins the chain view: its row, each
// member's binding view and the closed rounds a catch-up needs, the raw trace,
// and the server's record id and installation.
func TestServedChainViewCarriesRoundsAndTrace(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))

	b := chainBinding(t, rt, "shop")
	b.Serve.ClosedRound = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save the builder close: %v", err)
	}
	if err := rt.Store.AppendLog("shop", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Outcome: "done", Gate: &store.GateRecord{Result: "pass"},
	}); err != nil {
		t.Fatalf("append the builder report: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		return tx.ChainSaveWithEvent(row, db.ChainEventRow{
			TS: baseTime, Phase: "build", Step: "building", Member: "shop", Round: 1, Plan: 1,
			Event: "{}", Action: "{}",
		})
	}); err != nil {
		t.Fatalf("plant the trace row: %v", err)
	}

	view, err := ServedChainView(rt, "shop", func(string) string { return "rec-1" }, "install-1")
	if err != nil {
		t.Fatalf("ServedChainView: %v", err)
	}
	if view.Name != "shop" || view.Status != "running" || view.Settings.ReviewerActor != "reviewer" {
		t.Errorf("view = %+v, want shop running with the stored settings", view)
	}
	if len(view.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(view.Members))
	}
	builder := view.Members[0]
	if builder.Part != chain.MemberBuilder || builder.Name != "shop" || builder.Actor != "builder" {
		t.Errorf("builder member = %+v, want the builder part", builder)
	}
	if builder.View.ID != "rec-1" || builder.View.Installation != "install-1" || builder.View.ClosedRound != 1 {
		t.Errorf("builder view = %+v, want the closed round and the server's ids", builder.View)
	}
	if len(builder.Rounds) != 1 || builder.Rounds[0].Round != 1 ||
		builder.Rounds[0].ReportOutcome != "done" || builder.Rounds[0].GateResult != "pass" {
		t.Errorf("builder rounds = %+v, want round 1's facts", builder.Rounds)
	}
	if len(view.Trace) != 1 || view.Trace[0].Member != "shop" || view.Trace[0].Round != 1 {
		t.Errorf("trace = %+v, want the planted row", view.Trace)
	}
}
