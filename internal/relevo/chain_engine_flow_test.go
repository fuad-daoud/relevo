package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// flowSeedWorkflow renders a seed that names the plan copy beside the chain's
// branch, so both a path reference and an inline reference are exercised.
const flowSeedWorkflow = `name: seedpaths
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "work on {{plans.current}} on {{chain.branch}}", on: { done: done } }
`

// flowEmptyCheckWorkflow carries a check whose command renders empty, which is
// green with no run.
const flowEmptyCheckWorkflow = `name: emptycheck
inputs: { plans: required }
params: { nop: "" }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "{{params.nop}}", on: { green: done, red: { halt: "red" } } }
`

// flowCheckLogWorkflow runs a check and then a reader whose seed names the
// check's own sealed log.
const flowCheckLogWorkflow = `name: checklog
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "true", on: { green: report, red: { halt: "red" } } }
  report: { run: assistant, seed: "the log is at {{check.log}}", on: { verdict=pass: done, verdict=changes: { halt: "changes" } } }
`

// TestWorkflowSendRendersSeedPaths pins the seed rendering: a plan reference
// becomes an openable path holding the plan's bytes, and an inline reference
// renders in place.
func TestWorkflowSendRendersSeedPaths(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowSeedWorkflow)

	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", 1))
	if err != nil {
		t.Fatalf("read the builder prompt: %v", err)
	}
	const prefix, suffix = "work on ", " on relevo/shop"
	text := string(prompt)
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		t.Fatalf("prompt = %q, want %q<plan> %q", text, prefix, suffix)
	}
	planPath := strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	body, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("the seed's plan path %q is not openable: %v", planPath, err)
	}
	if string(body) != "build it" {
		t.Errorf("the seed's plan path holds %q, want the plan's bytes", body)
	}
}

// TestWorkflowCloseMissingArtifactHalts pins the missing-artifact arm: a reader
// whose declared artifact was not written closes the step halted, with the
// engine's reason naming the artifact.
func TestWorkflowCloseMissingArtifactHalts(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

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
		ev, cerr = chainEventFromCloseWF(rt, tx, c, b, chainCloseWF{
			Body: []byte("the review\n```relevo\nverdict: pass\n```\n"), Path: "", Outcome: "done",
		})
		return cerr
	}); err != nil {
		t.Fatalf("chainEventFromCloseWF: %v", err)
	}
	if ev.Status != "halted" || !strings.Contains(ev.Reason, "findings") {
		t.Fatalf("event = %s %q, want halted on the missing findings artifact", ev.Status, ev.Reason)
	}

	flowAdvance(t, rt, ev)
	got := flowChainRow(t, rt)
	if got.Status != string(workflow.StatusHalted) || !strings.Contains(got.Reason, "findings") {
		t.Errorf("row = %s %q, want halted naming the missing findings artifact", got.Status, got.Reason)
	}
}

// TestWorkflowEmptyCheckRoutesGreenWithoutRun pins the empty check: a check
// whose command renders empty routes green with no run started and no row.
func TestWorkflowEmptyCheckRoutesGreenWithoutRun(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowEmptyCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s (%q), want done from a green empty check", got.Status, got.Reason)
	}
	if _, err := rt.Store.ChainCheck("shop", 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an empty check started a run: %v", err)
	}
}

// TestWorkflowCheckLogReferenceable pins the check's log reference: after a
// green check, a later seed naming {{check.log}} renders to an openable copy of
// the run's sealed log.
func TestWorkflowCheckLogReferenceable(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := rt.Runner.(*fakeRunner)
	startFlowChain(t, rt, flowCheckLogWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	row, err := rt.Store.ChainCheck("shop", 1)
	if err != nil {
		t.Fatalf("ChainCheck: %v", err)
	}
	body := []byte("check output\nsecond line\n")
	if err := os.WriteFile(row.Log, body, 0o644); err != nil {
		t.Fatalf("write check log: %v", err)
	}
	fr.script(row.PID, false)
	fr.exit(row.PID, 0)
	tickChainChecks(context.Background(), rt)

	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-assistant", 1))
	if err != nil {
		t.Fatalf("read the reader's prompt: %v", err)
	}
	const prefix = "the log is at "
	if !strings.HasPrefix(string(prompt), prefix) {
		t.Fatalf("prompt = %q, want it to name the check log", prompt)
	}
	got, err := os.ReadFile(strings.TrimPrefix(string(prompt), prefix))
	if err != nil {
		t.Fatalf("the seed's log path is not openable: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("the seed's log copy = %q, want the sealed log %q", got, body)
	}
}
