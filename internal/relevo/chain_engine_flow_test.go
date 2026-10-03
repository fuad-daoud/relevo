package relevo

import (
	"context"
	"errors"
	"fmt"
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

// flowLocalRepairWorkflow runs a local check that routes a red straight to a
// repair round seeded by the shipped repair template, so the repair prompt's
// naming of the failed check's log is exercised.
const flowLocalRepairWorkflow = `name: repairflow
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "true", on: { green: done, red: repair } }
  repair: { run: builder, seed: shipped:repair, on: { done: done } }
`

// TestWorkflowLocalRepairSeedNamesTheCheckLog pins the repair seed's log input:
// a local red check routes to repair, and the prompt the builder is handed names
// the failed check's sealed log as an openable copy and carries its tail. A
// chain writer carries no gate, so the log must come from the engine's recorded
// check result.
func TestWorkflowLocalRepairSeedNamesTheCheckLog(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowLocalRepairWorkflow)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL the thing\n")

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want the repair round 2", builder.Round)
	}
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", builder.Round))
	if err != nil {
		t.Fatalf("read the repair prompt: %v", err)
	}
	text := string(prompt)
	const line = "Acceptance check output: "
	i := strings.Index(text, line)
	if i < 0 {
		t.Fatalf("the repair seed does not name the check log:\n%s", text)
	}
	named := strings.SplitN(text[i+len(line):], " -- last ", 2)[0]
	if !rt.Store.DiskRegularFile(named) {
		t.Errorf("the repair seed's check log %s is not a regular file on disk", named)
	}
	got, err := rt.Store.ReadFile(named)
	if err != nil {
		t.Fatalf("read the repair seed's check log %s: %v", named, err)
	}
	if !strings.Contains(string(got), "FAIL the thing") {
		t.Errorf("the repair seed's check log = %q, want the failed check's output", got)
	}
	if !strings.Contains(text, "FAIL the thing") {
		t.Errorf("the repair seed does not carry the log tail:\n%s", text)
	}
}

// TestWorkflowDirtyWriterCloseDoesNotAdvance pins the dirty gate: a writer
// member that closes done with uncommitted work still sitting in the tree it
// owns does not carry that work forward as a completed round. The chain halts,
// and the halt reason names the snapshot ref that holds the work and the diff
// key the store sealed, so the next builder finds evidence rather than a
// mystery.
func TestWorkflowDirtyWriterCloseDoesNotAdvance(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	fg.dirtyResult = true

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	c := flowChainRow(t, rt)
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step == "review" {
		t.Errorf("the chain advanced to review over an uncommitted tree: status %s", c.Status)
	}
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want halted: a dirty done close must not advance", c.Status, c.Reason)
	}
	for _, want := range []string{
		fmt.Sprintf("refs/relevo/shop/round-%d", 1),
		rt.Store.DiffPath("shop", 1),
	} {
		if !strings.Contains(c.Reason, want) {
			t.Errorf("halt reason %q does not name %s", c.Reason, want)
		}
	}
}

// TestWorkflowCleanWriterCloseStillAdvances pins the other half of the dirty
// gate: a clean close advances exactly as it did before the gate existed.
func TestWorkflowCleanWriterCloseStillAdvances(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	c := flowChainRow(t, rt)
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "review" || st.Awaiting.Member != "assistant" {
		t.Errorf("awaiting = %+v, want review/assistant from a clean close", st.Awaiting)
	}
	if c.Status != string(workflow.StatusRunning) {
		t.Errorf("status = %s (%q), want running", c.Status, c.Reason)
	}
}

// TestWorkflowDirtyReaderCloseKeepsItsRules pins the gate's scope: a reader
// close keeps its own rules, so a dirty scratch tree next to it says nothing
// about the round and does not halt its chain.
func TestWorkflowDirtyReaderCloseKeepsItsRules(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	fg.dirtyResult = true

	chainReaderClose(t, rt, "shop-assistant", chainVerdictBody("pass"))

	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s (%q), want done: the gate is the writer's alone", got.Status, got.Reason)
	}
}

// TestWorkflowDirtyGateNeverReadsAGitErrorAsClean pins the git-error arm: a
// dirty read that fails is not a clean tree, so the close does not advance on
// the guess. The reason says the tree could not be read rather than claiming it
// held nothing.
func TestWorkflowDirtyGateNeverReadsAGitErrorAsClean(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	fg.dirtyErr = errors.New("git: cannot read the index")

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want halted: an unreadable tree is not a clean one", c.Status, c.Reason)
	}
	if strings.Contains(c.Reason, "nothing") {
		t.Errorf("halt reason %q reads an unreadable tree as a clean one", c.Reason)
	}
}

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
