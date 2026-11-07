package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/workflow"
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

// TestServedChainPreflightRefusesAnArchivedName pins the archived reuse: a
// chain whose members were archived refuses the name before anything is
// written, with the archived message and no new live rows.
func TestServedChainPreflightRefusesAnArchivedName(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := servedChainRequest("shop")
	res := servedChainStart(t, rt, req)
	for _, m := range res.Members {
		if _, err := rt.Store.Archive(m.Name); err != nil {
			t.Fatalf("Archive %s: %v", m.Name, err)
		}
	}
	_, err := ServedChainPreflight(rt, req)
	if err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("err = %v, want the archived-chain refusal", err)
	}
	if !strings.Contains(err.Error(), `"shop"`) {
		t.Errorf("err = %q, want it naming the archived chain", err)
	}
	if !errors.Is(err, refusal.ErrRefused) {
		t.Errorf("err = %v, want errors.Is(err, refusal.ErrRefused)", err)
	}
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		if _, lerr := rt.Store.Load(name); !errors.Is(lerr, store.ErrNotFound) {
			t.Errorf("binding %q exists after the refusal: %v", name, lerr)
		}
		if ok, herr := rt.Store.HasArchived(name); herr != nil || !ok {
			t.Errorf("HasArchived(%s) = %t, %v; want the archived row untouched", name, ok, herr)
		}
	}
	stored, cerr := rt.Store.Chain("shop")
	if cerr != nil {
		t.Fatalf("Chain: %v", cerr)
	}
	if stored.ID != res.Chain.ID {
		t.Errorf("chain ID = %q, want the old row %q (no new row written)", stored.ID, res.Chain.ID)
	}
}

// TestServedChainPreflightRefusesAnAllGatedMemberPick pins the refuse-vs-queue
// decision of this round: a served chain whose member role has every serving
// candidate gated refuses the whole preflight with the resolve.ErrAllGated refusal --
// exactly what the local chain start does. chainResolveActors (chain_start.go)
// resolves every member's pick read-only before anything is created and
// returns the resolution error as the start's refusal; the served preflight's
// pick loop already returns each servedActorPick error, so propagating the
// refusal at its source makes it answer the same way. Queuing a member whose
// candidate is empty is what the plan calls the bug: the chain row would
// exist, plan 1 would admit, and the member's later round would have nothing
// to spawn, with the gate's refusal never answered to the client that named
// the chain.
func TestServedChainPreflightRefusesAnAllGatedMemberPick(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	// Every actor in the chain fixture places testClaudeRef, whose provider
	// is "test": one rate limit gates every member's only candidate.
	if err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: []availability.Entry{
		{Kind: availability.RateLimited, Subject: "test", At: baseTime, Source: "planner"},
	}}); err != nil {
		t.Fatalf("gate the only candidate's provider: %v", err)
	}

	_, err := ServedChainPreflight(rt, servedChainRequest("shop"))
	if !errors.Is(err, resolve.ErrAllGated) {
		t.Fatalf("err = %v, want the resolve.ErrAllGated refusal", err)
	}
	assertServedChainAbsent(t, rt, "shop")
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
		// A member directory that is a file refuses the row's transaction
		// after an earlier member was prepared, so the whole create unwinds.
		if err := os.WriteFile(rt.Store.Dir("shop-rev"), []byte("not a directory"), 0o644); err != nil {
			t.Fatalf("plant the blocking file: %v", err)
		}
		if _, err := ServedChainCreate(context.Background(), rt, plan, rt.Store.WorktreePath("shop")); err == nil {
			t.Fatal("ServedChainCreate = nil, want the transaction refusal")
		}
		assertServedChainAbsent(t, rt, "shop")
	})
}

// TestServedChainCreateMapsSettingsToDefaultParams pins the served create's
// settings mapping: the wire settings land on the shipped default's params, so
// the chain's stored definition is what its engine run reads.
func TestServedChainCreateMapsSettingsToDefaultParams(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := servedChainRequest("shop")
	req.Settings.Security = true
	req.Settings.SecurityActor = "security"
	req.Settings.Gate = "make test"
	req.Settings.Regate = 3
	servedChainStart(t, rt, req)

	row := chainStoredRow(t, rt, "shop")
	def, err := workflow.Parse(row.WorkflowJSON)
	if err != nil {
		t.Fatalf("parse stored workflow: %v", err)
	}
	for name, want := range map[string]string{
		"reviewer": "reviewer", "planner": "lite-planner",
		"security": "security", "gate": "make test",
	} {
		if got := def.Params[name].Str; got != want {
			t.Errorf("param %s = %q, want %q", name, got, want)
		}
	}
	if !def.Params["scan"].Bool {
		t.Error("scan = false, want true")
	}
	if got := def.Params["regate"].Int; got != 3 {
		t.Errorf("regate = %d, want 3", got)
	}
	if got := def.Params["max_corrections"].Int; got != 2 {
		t.Errorf("max_corrections = %d, want 2", got)
	}
}

// TestServedMemberNamesMatchClientMirror pins the member names a served create
// writes: they are exactly the names the 8b rule gives and the names the
// client's own mirror plans, so a resume never creates a second member for an
// actor the chain already runs.
func TestServedMemberNamesMatchClientMirror(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := servedChainRequest("shop")
	req.Settings.Security = true
	req.Settings.SecurityActor = "security"
	servedChainStart(t, rt, req)

	row := chainStoredRow(t, rt, "shop")
	def, err := workflow.Parse(row.WorkflowJSON)
	if err != nil {
		t.Fatalf("parse stored workflow: %v", err)
	}
	planned, err := chain.MemberNames("shop", def, rt.RoleRegistry().WorkflowActors())
	if err != nil {
		t.Fatalf("chain.MemberNames: %v", err)
	}
	engineNames := map[string]string{}
	for _, m := range planned {
		engineNames[m.Name] = m.Actor
	}
	mirrorNames := map[string]string{}
	for _, m := range chainMembersFor(ChainOptions{Name: "shop"}, chainSettingsFromWire(req.Settings), RepoSettings{}) {
		mirrorNames[m.name] = m.actor
	}
	if !reflect.DeepEqual(engineNames, mirrorNames) {
		t.Fatalf("engine names = %v, mirror names = %v, want them equal", engineNames, mirrorNames)
	}
	for name := range engineNames {
		if _, err := rt.Store.Load(name); err != nil {
			t.Errorf("served member %q is missing: %v", name, err)
		}
	}
}

// TestServedChainViewFillsLegacyFields pins the served view's vocabulary: a
// workflow chain's phase, step, plan, plans and corrections come from the
// engine state through workflow.LegacyView, not from the row's saved columns.
func TestServedChainViewFillsLegacyFields(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))

	// The row's legacy columns are overwritten so a view that read them would
	// report the wrong position.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Phase = "security"
		row.Step = "scanning"
		row.Plan, row.Plans, row.Corrections = 9, 9, 4
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("overwrite the columns: %v", err)
	}

	view, err := ServedChainView(rt, "shop", func(string) string { return "" }, "")
	if err != nil {
		t.Fatalf("ServedChainView: %v", err)
	}
	if view.Phase != "build" || view.Step != "building" || view.Plan != 1 || view.Plans != 1 || view.Corrections != 0 {
		t.Errorf("view legacy fields = %s/%s plan %d/%d corrections %d, want build/building 1/1 0",
			view.Phase, view.Step, view.Plan, view.Plans, view.Corrections)
	}
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

		// The builder's close routes build -> check (empty, so green with no
		// run) -> review, which queues the served reviewer's round.
		flowAdvance(t, rt, workflow.Event{
			Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done",
		})

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

// TestServedChainEndQueuesNoDelivery pins the delivery skip: a served chain
// writes its terminal row and trace but queues no end delivery, while a local
// chain still queues one.
// TestWorkflowDirtyServedWriterCloseHalts pins the served half of the dirty
// gate: a served writer member's close over a tree that still holds its work
// halts the step with the same wording a local writer's close gives -- what is
// uncommitted, the snapshot ref the server's own close cuts, and the sealed
// diff key -- instead of the completed step the engine would read from it. The
// close event is built on the server's own row, whose CWD is the served
// worktree that daemon can read, so the gate answers it like any owned tree.
func TestWorkflowDirtyServedWriterCloseHalts(t *testing.T) {
	t.Parallel()

	rt, fg, _ := servedChainRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))
	fg.dirtyResult = true

	var ev workflow.Event
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		b, err := tx.Load("shop")
		if err != nil {
			return err
		}
		ev, err = chainEventFromCloseWF(context.Background(), rt, tx, c, b, chainCloseWF{
			Body: []byte(chainDoneBody()), Outcome: "done", Round: 1,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ev.Status != reporttail.OutcomeHalted {
		t.Fatalf("event = %s %q, want halted: a dirty served close must not advance", ev.Status, ev.Reason)
	}
	for _, want := range []string{
		"refs/relevo/shop/round-1",
		rt.Store.DiffPath("shop", 1),
	} {
		if !strings.Contains(ev.Reason, want) {
			t.Errorf("halt reason %q does not name %s", ev.Reason, want)
		}
	}
}

// TestWorkflowCleanServedWriterCloseStillAdvances pins the other half: a clean
// served close is the completed step the engine reads, and driving it into the
// engine moves the chain exactly as before the gate covered the server's own
// tree.
func TestWorkflowCleanServedWriterCloseStillAdvances(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))

	var ev workflow.Event
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		b, err := tx.Load("shop")
		if err != nil {
			return err
		}
		ev, err = chainEventFromCloseWF(context.Background(), rt, tx, c, b, chainCloseWF{
			Body: []byte(chainDoneBody()), Outcome: "done", Round: 1,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ev.Status != "" && ev.Status != "done" {
		t.Fatalf("event = %s %q, want a completed step from a clean close", ev.Status, ev.Reason)
	}
	flowAdvance(t, rt, ev)
	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "review" {
		t.Errorf("awaiting = %+v (%q), want the review step from a clean close", st.Awaiting, c.Reason)
	}
}

func TestServedChainEndQueuesNoDelivery(t *testing.T) {
	t.Parallel()

	t.Run("a served chain queues nothing", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		servedChainStart(t, rt, servedChainRequest("shop"))

		// The builder's close routes build -> check (empty, so green) ->
		// review, and the reviewer's pass exhausts the one plan and finishes.
		flowAdvance(t, rt, workflow.Event{
			Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done",
		})
		flowAdvance(t, rt, workflow.Event{
			Kind: workflow.EventStepClosed, Step: "review", Member: "reviewer", Round: 1, Status: "done",
			Outcomes: map[string]string{"verdict": "pass"},
		})

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

// longServedChainRequest is servedChainRequest with seven plans, three
// corrections and the security phase, so every member's scaled cap exceeds the
// default.
func longServedChainRequest(name string) ServedChainRequest {
	req := servedChainRequest(name)
	req.Plans = make([]string, 7)
	for i := range req.Plans {
		req.Plans[i] = "build it"
	}
	req.Settings.MaxCorrections = 3
	req.Settings.Security = true
	req.Settings.SecurityActor = "security"
	return req
}

// TestServedChainCreateGivesEveryMemberAScaledCap pins that a served create
// stores reader members, not only the writer, with a cap scaled from the plans.
func TestServedChainCreateGivesEveryMemberAScaledCap(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := longServedChainRequest("shop")
	res := servedChainStart(t, rt, req)

	set := chainSettingsFromWire(req.Settings)
	for name, part := range map[string]string{
		"shop": chain.MemberBuilder, "shop-rev": chain.MemberReviewer,
		"shop-plan": chain.MemberPlanner, "shop-sec": chain.MemberSecurity,
	} {
		b := memberByName(t, res.Members, name)
		want := chainMemberRoundCap(part, len(req.Plans), set)
		if want <= store.DefaultRoundCap {
			t.Fatalf("test setup: %s want %d must exceed the default %d", part, want, store.DefaultRoundCap)
		}
		if b.RoundCap != want {
			t.Errorf("%s RoundCap = %d, want %d", name, b.RoundCap, want)
		}
	}
}

// TestServedChainResumeGivesAnAddedMemberAScaledCap pins that a security member
// a served resume adds carries the chain's scaled cap, not the default.
func TestServedChainResumeGivesAnAddedMemberAScaledCap(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := longServedChainRequest("shop")
	req.Settings.Security = false
	servedChainStart(t, rt, req)

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
		var st workflow.State
		if err := json.Unmarshal(row.StateJSON, &st); err != nil {
			return err
		}
		st.Status = workflow.StatusHalted
		stateJSON, err := json.Marshal(st)
		if err != nil {
			return err
		}
		row.StateJSON = stateJSON
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("halt the chain: %v", err)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Security: ptr(true)}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	want := chainMemberRoundCap(chain.MemberSecurity, len(req.Plans), chainSettingsFromWire(req.Settings))
	if want <= store.DefaultRoundCap {
		t.Fatalf("test setup: want %d must exceed the default %d", want, store.DefaultRoundCap)
	}
	if got := chainBinding(t, rt, "shop-sec").RoundCap; got != want {
		t.Errorf("shop-sec RoundCap = %d, want %d", got, want)
	}
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
		var st workflow.State
		if err := json.Unmarshal(row.StateJSON, &st); err != nil {
			return err
		}
		st.Status = workflow.StatusHalted
		stateJSON, err := json.Marshal(st)
		if err != nil {
			return err
		}
		row.StateJSON = stateJSON
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

// TestServedChainPreflightCustomWorkflow pins the preflight for a custom
// workflow: the hand-built request yields a plan whose members match
// chain.MemberNames.
func TestServedChainPreflightCustomWorkflow(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	const customYAML = `name: custom-flow
inputs:
  plans: required
start: b
steps:
  b:
    run: builder
    on:
      done: r
  r:
    run: reviewer
    on:
      verdict=pass: done
      else: done
`
	req := servedChainRequest("custom")
	req.Workflow = []byte(customYAML)
	req.ClientActorIDs = map[string]string{
		"builder":  "b-link-id",
		"reviewer": "r-link-id",
	}

	plan, err := ServedChainPreflight(rt, req)
	if err != nil {
		t.Fatalf("ServedChainPreflight: %v", err)
	}

	def, err := workflow.Parse(req.Workflow)
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	actors := rt.RoleRegistry().WorkflowActors()
	wantPlanned, err := chain.MemberNames(req.Name, def, actors)
	if err != nil {
		t.Fatalf("chain.MemberNames: %v", err)
	}
	keeper := chain.WriterKeeper(def, workflow.UsedActors(def), actors)
	wantMembers := chainWorkflowMembers(def, wantPlanned, keeper)

	if !reflect.DeepEqual(plan.members, wantMembers) {
		t.Errorf("plan.members = %+v, want %+v", plan.members, wantMembers)
	}
}

// servedPinRuntime is servedChainRuntime with the pin fixture's candidates:
// opencode and claude serve builder, claude and alt serve reviewer, so a
// builder pin and a reviewer re-pin each have a token to name.
func servedPinRuntime(t *testing.T) Runtime {
	t.Helper()

	rt, _, _ := servedChainRuntime(t)
	rt.Candidates = candidateSet(t, chainPinCandidatesJSON)
	rows := chainRows()
	rows["builder"] = roles.Row{Candidates: []string{"opencode/test/m", "claude/test/m"}}
	rows["reviewer"] = roles.Row{Candidates: []string{"claude/test/m", "claude/test/alt"}}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	return rt
}

// TestServedChainPreflightPinsMemberCandidate pins the served pin: a valid pin
// stores its token on the member, while a part without one keeps the server's
// own default pick.
func TestServedChainPreflightPinsMemberCandidate(t *testing.T) {
	t.Parallel()

	rt := servedPinRuntime(t)
	req := servedChainRequest("shop")
	req.Candidates = map[string]string{"builder": "opencode/test/m", "reviewer": "claude/test/alt"}
	servedChainStart(t, rt, req)

	if got := chainBinding(t, rt, "shop").BuilderCandidate; got != "opencode/test/m" {
		t.Errorf("builder candidate = %q, want the pinned opencode/test/m", got)
	}
	if got := chainBinding(t, rt, "shop-rev").BuilderCandidate; got != "claude/test/alt" {
		t.Errorf("reviewer candidate = %q, want the pinned claude/test/alt", got)
	}
	if got := chainBinding(t, rt, "shop-plan").BuilderCandidate; got != testClaudeRef {
		t.Errorf("planner candidate = %q, want the server's default pick %q", got, testClaudeRef)
	}
}

// TestServedChainPreflightPinsWorkflowMemberCandidate pins the workflow
// create's actor-keyed pins: the pin stored on the member is the one keyed by
// its actor.
func TestServedChainPreflightPinsWorkflowMemberCandidate(t *testing.T) {
	t.Parallel()

	rt := servedPinRuntime(t)
	const customYAML = `name: custom-flow
inputs:
  plans: required
start: b
steps:
  b:
    run: builder
    on:
      done: r
  r:
    run: reviewer
    on:
      verdict=pass: done
      else: done
`
	req := servedChainRequest("custom")
	req.Workflow = []byte(customYAML)
	req.Candidates = map[string]string{"reviewer": "claude/test/alt"}
	servedChainStart(t, rt, req)

	if got := chainBinding(t, rt, "custom-reviewer").BuilderCandidate; got != "claude/test/alt" {
		t.Errorf("reviewer candidate = %q, want the pinned claude/test/alt", got)
	}
	if got := chainBinding(t, rt, "custom").BuilderCandidate; got == "claude/test/alt" {
		t.Errorf("builder candidate = %q, want the server's default pick, not the reviewer pin", got)
	}
}

// windowedPinCandidatesJSON is chainPinCandidatesJSON with the first builder
// limited to a daily window that baseTime (15:33Z) falls inside, so a round
// sent at baseTime re-picks an unpinned builder onto it.
const windowedPinCandidatesJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"],"windows":{"only":[{"from":"14:00Z","to":"00:00Z"}]}},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"alt","roles":["reviewer"]}
]`

// TestServedChainBuilderPinSurvivesAWindowRepick pins a served builder pin
// through round 1: the first builder's window is open, so an unpinned member
// moves onto it when its round is sent, but a pinned one keeps the token the
// owner named, on the settings create and on the workflow create alike.
func TestServedChainBuilderPinSurvivesAWindowRepick(t *testing.T) {
	t.Parallel()

	windowed := func(t *testing.T) Runtime {
		rt := servedPinRuntime(t)
		rt.Candidates = candidateSet(t, windowedPinCandidatesJSON)
		rows := chainRows()
		rows["builder"] = roles.Row{Candidates: []string{"opencode/test/m", "claude/test/m"}}
		rows["reviewer"] = roles.Row{Candidates: []string{"claude/test/m", "claude/test/alt"}}
		rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
		return rt
	}
	const flow = `name: custom-flow
inputs:
  plans: required
start: b
steps:
  b:
    run: builder
    on:
      done: done
`
	for _, tc := range []struct {
		name     string
		workflow string
		pins     map[string]string
		want     string
		pinned   bool
	}{
		{name: "settings pin", pins: map[string]string{"builder": "claude/test/m"}, want: "claude/test/m", pinned: true},
		{name: "workflow pin", workflow: flow, pins: map[string]string{"builder": "claude/test/m"}, want: "claude/test/m", pinned: true},
		{name: "no pin follows the window", want: "opencode/test/m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rt := windowed(t)
			req := servedChainRequest("shop")
			req.Workflow = []byte(tc.workflow)
			req.Candidates = tc.pins
			servedChainStart(t, rt, req)

			b := chainBinding(t, rt, "shop")
			if b.QueuedAt.IsZero() {
				t.Fatal("round 1 is not queued, so its send never ran")
			}
			if b.BuilderCandidate != tc.want {
				t.Errorf("builder candidate after round 1's send = %q, want %q", b.BuilderCandidate, tc.want)
			}
			if b.BuilderPinned != tc.pinned {
				t.Errorf("BuilderPinned = %v, want %v", b.BuilderPinned, tc.pinned)
			}
		})
	}
}

// TestServedChainResumeBuilderPinSurvivesAWindowRepick pins a builder re-pin
// on a served resume: the chain started unpinned on the windowed builder, and
// the resume's pin is stored as pinned, so the resumed round's send keeps it.
func TestServedChainResumeBuilderPinSurvivesAWindowRepick(t *testing.T) {
	t.Parallel()

	rt := servedPinRuntime(t)
	rt.Candidates = candidateSet(t, windowedPinCandidatesJSON)
	rows := chainRows()
	rows["builder"] = roles.Row{Candidates: []string{"opencode/test/m", "claude/test/m"}}
	rows["reviewer"] = roles.Row{Candidates: []string{"claude/test/m", "claude/test/alt"}}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	servedChainStart(t, rt, servedChainRequest("shop"))
	haltServedChain(t, rt, "shop")

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"builder": "claude/test/m"},
	}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	b := chainBinding(t, rt, "shop")
	if b.BuilderCandidate != "claude/test/m" || !b.BuilderPinned {
		t.Errorf("builder after resume = %q pinned %v, want claude/test/m pinned", b.BuilderCandidate, b.BuilderPinned)
	}
}

// TestServedChainPreflightRefusesAnUnknownPin pins the served refusal: a pin
// serving only another actor refuses the preflight with bind's own wording,
// and nothing is created -- the default pick is never silently substituted.
func TestServedChainPreflightRefusesAnUnknownPin(t *testing.T) {
	t.Parallel()

	rt := servedPinRuntime(t)
	req := servedChainRequest("shop")
	req.Candidates = map[string]string{"builder": "claude/test/alt"}

	_, err := ServedChainPreflight(rt, req)
	if err == nil {
		t.Fatal("ServedChainPreflight(cross-part pin) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), `does not serve actor "builder"`) {
		t.Errorf("err = %q, want bind's does-not-serve-actor wording", err.Error())
	}
	assertServedChainAbsent(t, rt, "shop")
}

// TestServedChainPreflightRefusesAPinWithoutMember pins the unknown-key
// refusal: a pin naming a part no member fills is refused before anything is
// created, on both the settings and the workflow preflight.
func TestServedChainPreflightRefusesAPinWithoutMember(t *testing.T) {
	t.Parallel()

	t.Run("settings create", func(t *testing.T) {
		t.Parallel()

		rt := servedPinRuntime(t)
		req := servedChainRequest("shop")
		req.Candidates = map[string]string{"security": "claude/test/m"}

		_, err := ServedChainPreflight(rt, req)
		if err == nil {
			t.Fatal("ServedChainPreflight(security pin without a member) = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "has no security member to pin") {
			t.Errorf("err = %q, want it to name the missing security member", err.Error())
		}
		assertServedChainAbsent(t, rt, "shop")
	})

	t.Run("workflow create", func(t *testing.T) {
		t.Parallel()

		rt := servedPinRuntime(t)
		const customYAML = `name: custom-flow
inputs:
  plans: required
start: b
steps:
  b:
    run: builder
    on:
      done: r
  r:
    run: reviewer
    on:
      verdict=pass: done
      else: done
`
		req := servedChainRequest("custom")
		req.Workflow = []byte(customYAML)
		req.Candidates = map[string]string{"security": "claude/test/m"}

		_, err := ServedChainPreflight(rt, req)
		if err == nil {
			t.Fatal("ServedChainPreflight(security pin without a member) = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "has no security member to pin") {
			t.Errorf("err = %q, want it to name the missing security member", err.Error())
		}
		assertServedChainAbsent(t, rt, "custom")
	})
}

// haltServedChain leaves a served chain halted with its builder on round 2,
// the shape a collected close leaves for a resume to re-send.
func haltServedChain(t *testing.T, rt Runtime, name string) {
	t.Helper()

	builder := chainBinding(t, rt, name)
	builder.Round = 2
	if err := rt.Store.Save(builder); err != nil {
		t.Fatalf("advance the builder: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain(name)
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusHalted)
		var st workflow.State
		if err := json.Unmarshal(row.StateJSON, &st); err != nil {
			return err
		}
		st.Status = workflow.StatusHalted
		stateJSON, err := json.Marshal(st)
		if err != nil {
			return err
		}
		row.StateJSON = stateJSON
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("halt the chain: %v", err)
	}
}

// servedCandidates snapshots every served member's stored candidate.
func servedCandidates(t *testing.T, rt Runtime) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		out[name] = chainBinding(t, rt, name).BuilderCandidate
	}
	return out
}

// TestServedChainResumeRepinsReviewerCandidate pins the served re-pin: a
// resume with --candidate reviewer=<alt> stores the alt token on the served
// reviewer, while the other members keep their stored candidate, and the
// chain runs again.
func TestServedChainResumeRepinsReviewerCandidate(t *testing.T) {
	t.Parallel()

	rt := servedPinRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))
	before := servedCandidates(t, rt)
	haltServedChain(t, rt, "shop")

	res, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"reviewer": "claude/test/alt"},
	})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if res.Chain.Status != string(chain.StatusRunning) {
		t.Errorf("result chain status = %q, want running", res.Chain.Status)
	}

	if got := chainBinding(t, rt, "shop-rev").BuilderCandidate; got != "claude/test/alt" {
		t.Errorf("reviewer candidate = %q, want the re-pinned claude/test/alt", got)
	}
	after := servedCandidates(t, rt)
	for _, name := range []string{"shop", "shop-plan"} {
		if after[name] != before[name] {
			t.Errorf("%s candidate = %q, want the stored %q", name, after[name], before[name])
		}
	}
}

// TestServedChainResumeWithoutCandidateKeepsStoredCandidates pins the served
// default: a resume without --candidate keeps every stored candidate
// byte-for-byte.
func TestServedChainResumeWithoutCandidateKeepsStoredCandidates(t *testing.T) {
	t.Parallel()

	rt := servedPinRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))
	before := servedCandidates(t, rt)
	haltServedChain(t, rt, "shop")

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	if after := servedCandidates(t, rt); !reflect.DeepEqual(after, before) {
		t.Errorf("candidates = %v, want the stored %v", after, before)
	}
}

// TestServedChainResumeBadPinRefusesBeforeAnyWrite pins the served bad pin: a
// builder pin that serves only the reviewer refuses the resume with the
// server's own error, and the member rows and stored candidates are
// unchanged.
func TestServedChainResumeBadPinRefusesBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	rt := servedPinRuntime(t)
	servedChainStart(t, rt, servedChainRequest("shop"))
	before := servedCandidates(t, rt)
	rowsBefore, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers before: %v", err)
	}
	haltServedChain(t, rt, "shop")

	_, err = ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"builder": "claude/test/alt"},
	})
	if err == nil {
		t.Fatal("ChainResume(cross-part pin) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), `does not serve actor "builder"`) {
		t.Errorf("err = %q, want bind's does-not-serve-actor wording", err.Error())
	}

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Errorf("chain status = %q, want it still halted", row.Status)
	}
	rowsAfter, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers after: %v", err)
	}
	if !reflect.DeepEqual(rowsAfter, rowsBefore) {
		t.Errorf("member rows = %+v, want untouched %+v", rowsAfter, rowsBefore)
	}
	if after := servedCandidates(t, rt); !reflect.DeepEqual(after, before) {
		t.Errorf("candidates = %v, want untouched %v", after, before)
	}
}
