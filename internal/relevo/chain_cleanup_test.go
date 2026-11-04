package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// cleanupFlow is a build, a cleanup and a check: the cleanup must run between
// them and a failing cleanup must still reach the check.
const cleanupFlow = `name: cleanupflow
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: cleanup-round } }
  cleanup-round: { cleanup: "make clean", on: { done: check } }
  check: { check: "true", on: { green: done, red: { halt: "red" } } }
`

// cleanupRepairFlow is a cleanup before a check whose red buys a repair: a
// failing cleanup must never spend that budget.
const cleanupRepairFlow = `name: cleanuprepair
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: cleanup-round } }
  cleanup-round: { cleanup: "make clean", on: { done: check } }
  check: { check: "true", on: { green: done, red: repair } }
  repair: { run: builder, seed: shipped:repair, on: { done: done } }
`

func cleanupAwaitedStep(t *testing.T, rt Runtime) string {
	t.Helper()
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	return st.Awaiting.Step
}

// TestChainCleanupNonzeroExitAdvancesToDone pins the warning-never-failure
// rule: a cleanup that exits nonzero still closes as done and reaches the
// check, with the exit and a warning marker in the trace.
func TestChainCleanupNonzeroExitAdvancesToDone(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, cleanupFlow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	if got := cleanupAwaitedStep(t, rt); got != "cleanup-round" {
		t.Fatalf("awaiting = %q, want cleanup-round", got)
	}
	_, row := flowCheckRun(t, rt, "shop")
	if row.Step != "cleanup-round" {
		t.Fatalf("run step = %q, want cleanup-round", row.Step)
	}
	if !strings.Contains(row.Log, "cleanup-") || strings.Contains(row.Log, "check-") {
		t.Errorf("cleanup log = %q, want a cleanup- key, never a check- key", row.Log)
	}

	chainRedCheck(t, rt, "shop", "dirty tree\n")

	if got := cleanupAwaitedStep(t, rt); got != "check" {
		t.Fatalf("awaiting = %q, want the check after a failed cleanup", got)
	}
	got := flowChainRow(t, rt)
	if got.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want running past a failed cleanup", got.Status, got.Reason)
	}

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	text := RenderTrace(doc)
	if !strings.Contains(text, "cleanup-round run") || !strings.Contains(text, "exit=1") {
		t.Errorf("trace = %q, want the cleanup run with its exit", text)
	}
	if !strings.Contains(text, "warning: cleanup failed") {
		t.Errorf("trace = %q, want the nonzero-exit warning marker", text)
	}
}

// TestChainCleanupFailureNeverTriggersRepair pins that a failing cleanup
// spends no regate budget: the check that later fails red still buys its
// repair round.
func TestChainCleanupFailureNeverTriggersRepair(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, cleanupRepairFlow)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "cleanup blew up\n")

	if got := cleanupAwaitedStep(t, rt); got != "check" {
		t.Fatalf("awaiting = %q, want the check after a failed cleanup, not a repair", got)
	}
	chainRedCheck(t, rt, "shop", "check blew up\n")

	b := chainBinding(t, rt, "shop")
	if b.Round != 2 {
		t.Fatalf("builder round = %d, want the repair round 2: the cleanup must not have spent the budget", b.Round)
	}
	if got := flowChainRow(t, rt); got.Status == string(workflow.StatusHalted) {
		t.Errorf("status = halted (%q), want the repair to run after a failed cleanup", got.Reason)
	}
}

// TestChainCleanupRedNeverFeedsRepeatRed pins the explicit guard: a cleanup
// close with the same log as a previous red is still done without RepeatRed,
// so breaking the guard fails the test.
func TestChainCleanupRedNeverFeedsRepeatRed(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, cleanupFlow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	chainRedCheck(t, rt, "shop", "the same failure\n")

	row := chainLastCheckClosed(t, rt, "cleanup-round")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Result != "done" {
		t.Errorf("cleanup close result = %q, want done, never a check word", ev.Result)
	}
	if ev.RepeatRed {
		t.Error("cleanup close carries RepeatRed: a cleanup must never feed the repeat rule")
	}
}

// TestChainCleanupTickSkipsAwaitingCleanup pins the tick guard: a chain left
// awaiting a placed cleanup with no local run skips on tick, never polling
// GetCheck or reading a gate record.
func TestChainCleanupTickSkipsAwaitingCleanup(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, cleanupFlow)
	chainSetWorkflowState(t, rt, func(st *workflow.State) {
		st.Awaiting = workflow.Awaiting{Step: "cleanup-round", Run: 0}
	})
	chainPulledGate(t, rt, "fail")
	tickChainChecks(context.Background(), rt)

	if got := cleanupAwaitedStep(t, rt); got != "check" {
		t.Fatalf("awaiting = %q, want the check past a tick-skipped cleanup", got)
	}
	row := chainLastCheckClosed(t, rt, "cleanup-round")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Log != "" || ev.Result != "done" {
		t.Errorf("tick-skipped cleanup = result %q log %q, want done with no log", ev.Result, ev.Log)
	}
}

// TestChainCleanupPullNeverAnswersFromGate pins the Pull guard directly: on
// a server without the check route, a placed cleanup still skips without
// reading the writer's gate record. Letting Pull answer from the gate would
// close the cleanup with the gate's log instead of the skip.
func TestChainCleanupPullNeverAnswersFromGate(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, cleanupFlow)
	// The server loses the check route before the cleanup is entered, so the
	// FlowCheck falls through to Pull, which must still skip.
	rt.Remote = chainRemoteFake()
	chainPulledGate(t, rt, "fail")
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	if got := cleanupAwaitedStep(t, rt); got != "check" {
		t.Fatalf("awaiting = %q, want the check past a Pull-skipped cleanup", got)
	}
	row := chainLastCheckClosed(t, rt, "cleanup-round")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Log != "" {
		t.Errorf("Pull-skipped cleanup log = %q, want empty: never the gate record", ev.Log)
	}
	if ev.Result != "done" {
		t.Errorf("Pull-skipped cleanup result = %q, want done, never the gate's word", ev.Result)
	}
}

// TestChainCleanupPlacedSkipsWithoutGate pins the placed-writer skip: a
// cleanup on a remote writer never reads the writer's gate record and never
// posts a check run for the cleanup step, advancing past it with a trace
// warning instead. A later check still runs through the check route.
func TestChainCleanupPlacedSkipsWithoutGate(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, cleanupFlow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	if req := fr.createCheckReq; req.Step == "cleanup-round" {
		t.Fatalf("cleanup posted to the check route: %+v", req)
	}
	// A red gate record the cleanup must never stand in for.
	chainPulledGate(t, rt, "fail")

	tickChainChecks(context.Background(), rt)

	if _, err := rt.Store.ChainCheck("shop", 1); !errors.Is(err, store.ErrNotFound) {
		// A check run may exist for the later check step, but never for the
		// cleanup: the cleanup step must not have started a local run.
		if row, rerr := rt.Store.ChainCheck("shop", 1); rerr == nil && row.Step == "cleanup-round" {
			t.Errorf("a local cleanup run exists on a placed writer: %+v", row)
		}
	}
	if got := cleanupAwaitedStep(t, rt); got != "check" {
		t.Fatalf("awaiting = %q, want the check past a skipped placed cleanup", got)
	}
	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if text := RenderTrace(doc); !strings.Contains(text, "cleanup-round skipped") {
		t.Errorf("trace = %q, want the placed skip with its warning", text)
	}
	// The red gate record must not have closed the cleanup: the skip carries
	// no gate log.
	row := chainLastCheckClosed(t, rt, "cleanup-round")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Log != "" {
		t.Errorf("skipped cleanup log = %q, want empty: never the gate record", ev.Log)
	}
}

// TestChainCleanupAndCheckLogsDoNotCollide pins the log separation: a round
// that runs both a check and a cleanup holds two files, and show --gate reads
// the check log, never the cleanup log.
func TestChainCleanupAndCheckLogsDoNotCollide(t *testing.T) {
	t.Parallel()

	s := newShowLiveStore(t)
	putCheckLog(t, s, "fixture", 2, 1, "check output\n")
	member, round := "fixture", 2
	cleanupKey := s.CleanupLogPath(member, round, 1)
	if !strings.Contains(cleanupKey, "cleanup-") || strings.Contains(cleanupKey, "check-") {
		t.Fatalf("cleanup key = %q, want a cleanup- name that is never a check- name", cleanupKey)
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile(member, round, cleanupKey, []byte("cleanup output\n"))
	}); err != nil {
		t.Fatalf("PutRoundFile cleanup: %v", err)
	}

	rt := Runtime{Store: s}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing {
		t.Fatal("Missing = true, want the round's chain check log")
	}
	if !strings.Contains(res.Text, "check output") || strings.Contains(res.Text, "cleanup output") {
		t.Errorf("Text = %q, want only the check log, never the cleanup log", res.Text)
	}
}

// TestCleanupTraceWarnsOnNonzeroExit pins the trace wording: a cleanup close
// with a nonzero exit renders its step, run, exit, duration and warning.
func TestCleanupTraceWarnsOnNonzeroExit(t *testing.T) {
	t.Parallel()

	e := ChainTraceEvent{
		Step:       "cleanup-round",
		Flow:       &workflow.Event{Kind: workflow.EventCheckClosed, Step: "cleanup-round", Run: 2, Result: "done", Log: "k", ExitCode: 1, DurationMS: 1500},
		FlowAction: &workflow.Action{Kind: workflow.ActionRunCheck, Step: "check"},
	}
	got := flowTraceLine(e)
	for _, want := range []string{"cleanup-round", "run 2", "exit=1", "warning"} {
		if !strings.Contains(got, want) {
			t.Errorf("trace line = %q, want it to contain %q", got, want)
		}
	}
	if !strings.Contains(got, "1.5s") && !strings.Contains(got, "1500ms") {
		t.Errorf("trace line = %q, want the duration beside the exit", got)
	}

	ok := ChainTraceEvent{
		Step:       "cleanup-round",
		Flow:       &workflow.Event{Kind: workflow.EventCheckClosed, Step: "cleanup-round", Run: 2, Result: "done", Log: "k", ExitCode: 0, DurationMS: 200},
		FlowAction: &workflow.Action{Kind: workflow.ActionRunCheck, Step: "check"},
	}
	if got := flowTraceLine(ok); strings.Contains(got, "warning") {
		t.Errorf("passing cleanup line = %q, want no warning marker on exit 0", got)
	}
}

// TestCleanupStatusWarnsWhileDone pins the status warning: a done chain whose
// cleanup failed still surfaces the failure beside its state.
func TestCleanupStatusWarnsWhileDone(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, cleanupFlow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	chainRedCheck(t, rt, "shop", "cleanup blew up\n")
	chainGreenCheck(t, rt, "shop")

	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Fatalf("status = %s (%q), want done after a failed cleanup and a green check", got.Status, got.Reason)
	}
	rep, err := ChainStatus(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStatus: %v", err)
	}
	if len(rep.Bindings) == 0 {
		t.Fatal("no rows")
	}
	if got := rep.Bindings[0].Detail; !strings.Contains(got, "cleanup-round") || !strings.Contains(got, "exit 1") {
		t.Errorf("Detail = %q, want the failed cleanup warning beside the done state", got)
	}
}

// TestSkippedCleanupLeavesNoCheckRow pins the empty case: a chain with no
// cleanup stores today's shape with no cleanup steps, runs no cleanup and
// writes no check row for one.
func TestSkippedCleanupLeavesNoCheckRow(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowCheckWorkflow)
	def, err := chain.WorkflowDef(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("WorkflowDef: %v", err)
	}
	for id, step := range def.Steps {
		if step.Cleanup != "" {
			t.Fatalf("stored step %q carries a cleanup, want today's pruned shape", id)
		}
	}
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	if got := cleanupAwaitedStep(t, rt); got != "check" {
		t.Fatalf("awaiting = %q, want the check with no cleanup step in between", got)
	}
}

// TestFlowCheckResultIgnoresCleanup pins the no-change rule: the review and
// repair seeds read the gate from the engine's green or red result, never
// from a cleanup's done.
func TestFlowCheckResultIgnoresCleanup(t *testing.T) {
	t.Parallel()

	st := workflow.State{Results: map[string]workflow.Result{
		"cleanup-round": {Status: "done", Artifacts: map[string][]string{"log": {"k"}}},
	}}
	if result, log := flowCheckResult(st); result != "" || log != "" {
		t.Errorf("flowCheckResult with only a cleanup = (%q, %q), want empty: a cleanup is never the gate", result, log)
	}
}

// oldWorkflowServer is a server with workflow support but without cleanup:
// the shape a zen still on the old version reports.
func oldWorkflowServer() *fakeRemote {
	fr := chainServerFake()
	fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureWorkflow)
	return fr
}

// TestClientPruneNoCleanupMatchesTodayShape pins the old-server interop: a
// new client with no cleanup posts bytes equal to today's pruned shape, with
// no cleanup key an old server's onlyKeys would refuse. Removing the client
// prune makes this test fail.
func TestClientPruneNoCleanupMatchesTodayShape(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	opts := ChainOptions{Name: "shop", Plans: []string{writePlan(t, "build it")}, Feature: "auth"}
	def := workflow.Default()
	plan, err := chainServerResolveBodies(context.Background(), rt, opts, def, "shipped", t.TempDir(), RepoSettings{})
	if err != nil {
		t.Fatalf("ResolveBodies: %v", err)
	}
	raw, err := json.Marshal(plan.def)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "cleanup") {
		t.Errorf("posted workflow = %s, want today's shape with no cleanup key", raw)
	}
	if _, err := workflow.Parse(raw); err != nil {
		t.Fatalf("posted bytes do not parse: %v", err)
	}
	// The pruned shape is the fixed point: pruning again changes nothing.
	if repruned := workflow.PruneEmptyCleanup(plan.def); len(repruned.Steps) != len(plan.def.Steps) {
		t.Error("resolved def is not pruned: pruning again drops steps")
	}
}

// TestClientCleanupWarnsAndPrunesOnOldServer pins the fallback: with a
// cleanup set, a server without FeatureCleanup gets a pruned definition and
// a start-line warning naming it, and the chain runs without cleanup.
func TestClientCleanupWarnsAndPrunesOnOldServer(t *testing.T) {
	t.Parallel()

	fr := oldWorkflowServer()
	rt, _, _ := chainServerRuntime(t, fr)
	opts := chainServerOpts(t)
	opts.Cleanup = "make clean"
	opts.CleanupAfter = "chain"
	def := workflow.Default()
	plan, err := chainServerResolveBodies(context.Background(), rt, opts, def, "shipped", t.TempDir(), RepoSettings{})
	if err != nil {
		t.Fatalf("ResolveBodies: %v", err)
	}
	if !hasCleanupCommand(plan.def) {
		t.Fatal("resolved def carries no cleanup, want the set command before the old-server prune")
	}
	gated, who, err := chainServerResolveGates(context.Background(), rt, opts, plan, "shipped")
	if err != nil {
		t.Fatalf("ResolveGates: %v", err)
	}
	if remote.HasFeature(who, remote.FeatureCleanup) {
		t.Fatal("test server advertises cleanup, want the old shape without it")
	}
	if gated.cleanupWarning == "" || !strings.Contains(gated.cleanupWarning, "zen") {
		t.Errorf("warning = %q, want it to name the server", gated.cleanupWarning)
	}
	if hasCleanupCommand(gated.def) {
		t.Error("pruned def still carries a cleanup, want it cleared for the old server")
	}
	raw, _ := json.Marshal(chainServerRequest(opts, gated, chainServerIDs{}).Workflow)
	if strings.Contains(string(raw), "cleanup") {
		t.Errorf("posted workflow = %s, want no cleanup key for the old server", raw)
	}
}

// TestServerStoresAndRunsPostedCleanup pins the new-server path: a posted
// workflow with a cleanup step parses, survives the served prune and is
// stored, so the server's engine reaches the cleanup step.
func TestServerStoresAndRunsPostedCleanup(t *testing.T) {
	t.Parallel()

	const customCleanup = `name: cleanupserve
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: cleanup-round } }
  cleanup-round: { cleanup: "make clean", on: { done: done } }
`
	parsed, err := workflow.Parse([]byte(customCleanup))
	if err != nil {
		t.Fatalf("parse posted cleanup: %v", err)
	}
	if !hasCleanupCommand(workflow.PruneEmptyCleanup(parsed)) {
		t.Fatal("pruned posted def carries no cleanup, want it kept for the new server")
	}

	rt, _, _ := servedChainRuntime(t)
	req := servedChainRequest("shop")
	req.Workflow = []byte(customCleanup)
	req.ClientActorIDs = map[string]string{"builder": "b-link"}
	plan, err := ServedChainPreflight(rt, req)
	if err != nil {
		t.Fatalf("ServedChainPreflight with cleanup: %v", err)
	}
	if !hasCleanupCommand(plan.def) {
		t.Fatal("served plan carries no cleanup, want the posted step stored")
	}
	res := servedChainStart(t, rt, req)
	stored, err := chain.WorkflowDef(res.Chain)
	if err != nil {
		t.Fatalf("WorkflowDef: %v", err)
	}
	if !hasCleanupCommand(stored) {
		t.Fatal("stored def carries no cleanup, want the new server to keep it")
	}
}
