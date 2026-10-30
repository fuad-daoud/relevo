package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

const flowPlanWorkflow = `name: planflow
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: correct } }
  correct: { run: lite-planner, seed: shipped:correct, on: { done: rebuild } }
  rebuild: { run: builder, seed: "{{correct.plan}}", on: { done: done } }
`

func planStepCloseEvent(t *testing.T, body string) (workflow.Event, string) {
	t.Helper()
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowPlanWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	b, err := rt.Store.Load("shop-lite-planner")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var ev workflow.Event
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, cerr := tx.Chain("shop")
		if cerr != nil {
			return cerr
		}
		ev, cerr = chainEventFromCloseWF(context.Background(), rt, tx, c, b, chainCloseWF{Body: []byte(body), Path: path, Outcome: "done"})
		return cerr
	}); err != nil {
		t.Fatalf("chainEventFromCloseWF: %v", err)
	}
	flowAdvance(t, rt, ev)
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	return ev, st.Awaiting.Step
}

func TestChainCloseHaltsCorrectionStepWithoutPlanSteps(t *testing.T) {
	t.Parallel()
	ev, awaiting := planStepCloseEvent(t, "The plan is at /tmp/plan.md.")
	if ev.Status != "halted" {
		t.Fatalf("status = %q, want halted", ev.Status)
	}
	if !strings.Contains(ev.Reason, "step correct of chain shop") || !strings.Contains(ev.Reason, "no plan steps") {
		t.Errorf("reason = %q, want it to name the step and chain", ev.Reason)
	}
	if awaiting == "rebuild" {
		t.Errorf("chain awaits %q: the plan was sent to the builder", awaiting)
	}
}

func TestChainCloseAdvancesCorrectionStepWithSteps(t *testing.T) {
	t.Parallel()
	ev, awaiting := planStepCloseEvent(t, "1. Fix the thing\n2. Test it\n")
	if ev.Status != "done" {
		t.Fatalf("status = %q (%q), want done", ev.Status, ev.Reason)
	}
	if awaiting != "rebuild" {
		t.Errorf("chain awaits %q, want rebuild: the plan reached the builder", awaiting)
	}
}
