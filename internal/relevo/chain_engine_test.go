package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// flowReviewWorkflow is the two-step workflow the engine tests run: a plan is
// built, then an assistant reviews it and a changes verdict halts.
const flowReviewWorkflow = `name: twostep
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: review } }
  review: { run: assistant, seed: "review the build", on: { verdict=pass: done, verdict=changes: { halt: "changes" } } }
`

// flowCheckWorkflow is the check workflow: a plan is built and a green check
// finishes the chain.
const flowCheckWorkflow = `name: checkflow
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "true", on: { green: done, red: { halt: "red" } } }
`

// writeWorkflowFile writes a workflow definition to a temp file, the form a
// --workflow path takes.
func writeWorkflowFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

// startFlowChain starts shop on a workflow and returns what the start produced.
func startFlowChain(t *testing.T, rt Runtime, body string) ChainResult {
	t.Helper()
	return startedChain(t, rt, ChainOptions{Workflow: writeWorkflowFile(t, body)})
}

// flowChainRow loads shop's chain row.
func flowChainRow(t *testing.T, rt Runtime) db.ChainRow {
	t.Helper()
	c, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	return c
}

// flowAdvance drives the chain with one workflow event under the state lock.
func flowAdvance(t *testing.T, rt Runtime, ev workflow.Event) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		return chainAdvance(context.Background(), rt, tx, c, ev)
	}); err != nil {
		t.Fatalf("chainAdvance(%+v): %v", ev, err)
	}
}

// flowTraceRows is shop's trace length.
func flowTraceRows(t *testing.T, rt Runtime) int {
	t.Helper()
	rows, err := rt.Store.ChainEvents("shop")
	if err != nil {
		t.Fatalf("ChainEvents: %v", err)
	}
	return len(rows)
}

// TestWorkflowChainMembersRunNoGate pins a workflow start: one member per
// actor, the definition and state stored, the writer's round open and no gate
// of its own.
func TestWorkflowChainMembersRunNoGate(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	res := startFlowChain(t, rt, flowReviewWorkflow)

	if len(res.Members) != 2 {
		t.Fatalf("members = %d, want 2 (builder and assistant)", len(res.Members))
	}
	builder := memberByName(t, res.Members, "shop")
	if builder.Shape != store.ShapeWriter {
		t.Errorf("builder shape = %q, want writer", builder.Shape)
	}
	if builder.Gate != "" || builder.Regate != 0 {
		t.Errorf("builder gate = %q/%d, want none and 0: a workflow chain runs its checks as steps", builder.Gate, builder.Regate)
	}
	assistant := memberByName(t, res.Members, "shop-assistant")
	if assistant.Shape != store.ShapeReader {
		t.Errorf("assistant shape = %q, want reader", assistant.Shape)
	}

	c := flowChainRow(t, rt)
	if len(c.WorkflowJSON) == 0 || len(c.StateJSON) == 0 {
		t.Fatalf("row carries workflow %d and state %d bytes, want both", len(c.WorkflowJSON), len(c.StateJSON))
	}
	if c.Status != string(workflow.StatusRunning) || c.AwaitingRound != 1 {
		t.Errorf("row status/awaiting = %s/%d, want running and round 1", c.Status, c.AwaitingRound)
	}
	entries, err := rt.Store.ReadLog("shop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindPrompt && e.Round == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("builder log = %+v, want a prompt entry for round 1", entries)
	}
}

// TestWorkflowCloseParsesDeclaredOutcomes pins the close path: the builder's
// done close routes to the reviewer, and the reviewer's declared verdict is
// parsed and routed.
func TestWorkflowCloseParsesDeclaredOutcomes(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	c := flowChainRow(t, rt)
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "review" || st.Awaiting.Member != "assistant" || st.Awaiting.Round != 1 {
		t.Fatalf("awaiting = %+v, want review/assistant round 1", st.Awaiting)
	}

	body := []byte("the review\n```relevo\nverdict: changes\n```\n")
	report := filepath.Join(t.TempDir(), "findings.md")
	if err := os.WriteFile(report, body, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := rt.Store.Load("shop-assistant")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var ev workflow.Event
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		ev, err = chainEventFromCloseWF(rt, tx, c, b, chainCloseWF{Body: body, Path: report, Outcome: "done"})
		return err
	}); err != nil {
		t.Fatalf("chainEventFromCloseWF: %v", err)
	}
	if ev.Outcomes["verdict"] != "changes" {
		t.Fatalf("outcomes = %+v, want verdict=changes", ev.Outcomes)
	}
	flowAdvance(t, rt, ev)

	c = flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) || !strings.Contains(c.Reason, "changes") {
		t.Errorf("row = %s %q, want halted naming changes", c.Status, c.Reason)
	}
}

// TestWorkflowCloseReplayedIsIgnored pins the engine's replay guard: a close
// that no longer names the awaited step changes nothing and writes no row.
func TestWorkflowCloseReplayedIsIgnored(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	ev := workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"}
	flowAdvance(t, rt, ev)
	before := flowTraceRows(t, rt)
	flowAdvance(t, rt, ev)
	if after := flowTraceRows(t, rt); after != before {
		t.Errorf("trace rows = %d, want %d: a replayed close writes nothing", after, before)
	}
}

// TestWorkflowCheckGreenRoutesGreen pins the check action and its tick: a green
// check feeds check_closed and finishes the chain.
func TestWorkflowCheckGreenRoutesGreen(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := rt.Runner.(*fakeRunner)
	startFlowChain(t, rt, flowCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" || st.Awaiting.Member != "" {
		t.Fatalf("awaiting = %+v, want the check step", st.Awaiting)
	}
	row, err := rt.Store.ChainCheck("shop", 1)
	if err != nil {
		t.Fatalf("ChainCheck: %v", err)
	}
	if row.Step != "check" || row.Command != "true" {
		t.Fatalf("check row = %+v, want step check and command true", row)
	}

	fr.script(row.PID, false)
	fr.exit(row.PID, 0)
	tickChainChecks(context.Background(), rt)

	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s (%q), want done after a green check", got.Status, got.Reason)
	}
}

// TestShowWorkflowPrintsStoredDefinition pins `show --workflow`: the stored
// definition prints as JSON, and a binding that is not a workflow chain is
// refused.
func TestShowWorkflowPrintsStoredDefinition(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	res, err := Show(context.Background(), rt, ShowOptions{Name: "shop", Section: ShowWorkflow})
	if err != nil {
		t.Fatalf("Show --workflow: %v", err)
	}
	if !strings.Contains(res.Text, `"name": "twostep"`) {
		t.Errorf("workflow text = %q, want the stored definition", res.Text)
	}
	if _, err := Show(context.Background(), rt, ShowOptions{Name: "shop-assistant", Section: ShowWorkflow}); err == nil {
		t.Error("Show --workflow on a non-chain = nil error, want a refusal")
	}
}

// TestChainSegmentStepRoundPlans pins the workflow chain's status segment: the
// step its engine is on with the round it awaits, and its plan position.
func TestChainSegmentStepRoundPlans(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	f := chainFactsOf(rt.Store, flowChainRow(t, rt))
	if f.StepAt != "review" || f.Round != 1 || f.PlanPos != 1 || f.PlanTotal != 1 || f.Check {
		t.Fatalf("facts = %+v, want review r1 plans 1/1", f)
	}
	if got := view.ChainSegment(f); got != "review r1 · plans 1/1" {
		t.Errorf("segment = %q, want review r1 · plans 1/1", got)
	}
}

// TestWorkflowCheckRedHalts pins the red arm of a check: a failing check
// routes to the workflow's halt target.
func TestWorkflowCheckRedHalts(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := rt.Runner.(*fakeRunner)
	startFlowChain(t, rt, flowCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	row, err := rt.Store.ChainCheck("shop", 1)
	if err != nil {
		t.Fatalf("ChainCheck: %v", err)
	}
	fr.script(row.PID, false)
	fr.exit(row.PID, 2)
	tickChainChecks(context.Background(), rt)

	got := flowChainRow(t, rt)
	if got.Status != string(workflow.StatusHalted) || !strings.Contains(got.Reason, "red") {
		t.Errorf("row = %s %q, want halted naming red", got.Status, got.Reason)
	}
}

// TestWorkflowReviewPassFinishes pins the finish path: a pass verdict routes
// to the workflow's done target.
func TestWorkflowReviewPassFinishes(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	body := []byte("the review\n```relevo\nverdict: pass\n```\n")
	report := filepath.Join(t.TempDir(), "findings.md")
	if err := os.WriteFile(report, body, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := rt.Store.Load("shop-assistant")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var ev workflow.Event
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, cerr := tx.Chain("shop")
		if cerr != nil {
			return cerr
		}
		ev, cerr = chainEventFromCloseWF(rt, tx, c, b, chainCloseWF{Body: body, Path: report, Outcome: "done"})
		return cerr
	}); err != nil {
		t.Fatalf("chainEventFromCloseWF: %v", err)
	}
	flowAdvance(t, rt, ev)
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s, want done after a pass verdict", got.Status)
	}
}

// TestWorkflowResumeFromStep pins a workflow resume: a halted chain re-enters
// the named step and sends its member again.
func TestWorkflowResumeFromStep(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	// Halt on the review: a status the workflow does not match ends the chain.
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "review", Member: "assistant", Round: 1, Status: "halted", Reason: "no"})
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s, want halted", got.Status)
	}
	// Close the builder's round so the resume can open the next one: its report
	// entry and a dead process, as a closed local round leaves behind.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.AppendLog("shop", store.LogEntry{TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md"}); err != nil {
			return err
		}
		b, err := tx.Load("shop")
		if err != nil {
			return err
		}
		b.Builder = clearProcess(b.Builder)
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("close builder round: %v", err)
	}

	res, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", From: "build"})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if res.Chain.Status != string(workflow.StatusRunning) {
		t.Fatalf("resumed status = %s, want running", res.Chain.Status)
	}
	st, err := chainWorkflowState(res.Chain)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "build" || st.Awaiting.Member != "builder" {
		t.Errorf("awaiting = %+v, want build/builder", st.Awaiting)
	}
}

// TestRenderTraceWorkflowRow pins the workflow trace line: the closing step,
// its round and the target it chose.
func TestRenderTraceWorkflowRow(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	text := RenderTrace(doc)
	if !strings.Contains(text, "build r1") || !strings.Contains(text, "→ review") {
		t.Errorf("trace = %q, want a build r1 row routing to review", text)
	}
}
