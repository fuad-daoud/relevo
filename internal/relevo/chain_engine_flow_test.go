package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/roles"
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
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
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
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
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

// startNoRegateFlowChain starts the shipped default workflow with no repair
// budget: regate renders to zero, so a red check buys no repair round and the
// repair budget's then target is taken on the red itself. The gate is a command
// so a check really runs, and the scan is off so the walk stops inside the plan.
func startNoRegateFlowChain(t *testing.T, rt Runtime) ChainResult {
	t.Helper()
	body, err := json.Marshal(workflow.Default())
	if err != nil {
		t.Fatalf("encode the shipped workflow: %v", err)
	}
	return startedChain(t, rt, ChainOptions{
		Workflow: writeWorkflowFile(t, string(body)),
		Params:   map[string]string{"regate": "0", "gate": "make check", "scan": "false"},
	})
}

// TestWorkflowDirectFixChangesSendsFixDirectWithFindings pins the direct-fix
// loop: a changes verdict sends the builder a fix-direct round whose prompt
// renders the fix-direct seed with the reviewer's output path, and a second
// changes on the same plan spends the one fix-direct visit the plan allows and
// reaches the correct step.
func TestWorkflowDirectFixChangesSendsFixDirectWithFindings(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Workflow: "direct-fix"})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want the fix-direct round 2", builder.Round)
	}
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", builder.Round))
	if err != nil {
		t.Fatalf("read the fix-direct prompt: %v", err)
	}
	text := string(prompt)
	if !strings.Contains(text, "Fix the reviewer's findings directly, without a new plan.") {
		t.Errorf("fix-direct prompt does not render the fix-direct seed:\n%s", text)
	}
	rev := chainBinding(t, rt, "shop-rev")
	output := seedCopy(t, rt, rt.Store.OutputPath("shop-rev", 1, rev.WriterRole(), readerOutputLabel(rt, rev)))
	if !strings.Contains(text, "Reviewer's output: "+output+".") {
		t.Errorf("fix-direct prompt does not name the reviewer's output %s:\n%s", output, text)
	}

	// The fix-direct round closes done and its empty check goes green, so the
	// reviewer judges again; a second changes spends the one fix-direct visit
	// the plan allows and reaches the correct step.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "correct" {
		t.Errorf("awaiting = %+v, want the correct step after two changes on one plan", st.Awaiting)
	}
	if planner := chainBinding(t, rt, "shop-plan"); planner.Round != 1 {
		t.Errorf("planner round = %d, want the correct round 1", planner.Round)
	}
	reviewPrompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", 2))
	if err != nil {
		t.Fatalf("read the second review prompt: %v", err)
	}
	if want := "This closing round is a direct-fix round, on top of round 1."; !strings.Contains(string(reviewPrompt), want) {
		t.Errorf("second review prompt does not name %q:\n%s", want, reviewPrompt)
	}

	// The trace names the fix-direct round by its step id, as every other
	// step, so no step list needs a fix-direct entry.
	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if rendered := RenderTrace(doc); !strings.Contains(rendered, "fix-direct r2") {
		t.Errorf("trace does not name the fix-direct round:\n%s", rendered)
	}
}

// TestWorkflowRedCheckWithNoRegateNeverReachesReview pins the red-budget gate
// on the shipped workflow, end to end: a red check with no repair budget behind
// it must not send the plan to a reviewer whose pass verdict finishes it. The
// chain halts, or routes the round to the correction step instead.
func TestWorkflowRedCheckWithNoRegateNeverReachesReview(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL the gate\n")

	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step == "review" {
		t.Errorf("the red check sent the plan to review with no repair budget: %s (%q)", c.Status, c.Reason)
	}
	switch {
	case c.Status == string(workflow.StatusHalted):
	case st.Awaiting.Step == "correct":
	default:
		t.Errorf("status = %s (%q), awaiting %+v: want a halt or a correct round", c.Status, c.Reason, st.Awaiting)
	}
	if got := st.Results["check"].Status; got != chainCheckRed {
		t.Errorf("check result = %q, want the red recorded", got)
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
		ev, cerr = chainEventFromCloseWF(context.Background(), rt, tx, c, b, chainCloseWF{
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

// TestWorkflowUnclosedStatusBlockCloseReachesCheck pins that a builder report
// whose status block runs to end of file closes the round done and advances the
// chain to its check, instead of halting on an unclosed fence.
func TestWorkflowUnclosedStatusBlockCloseReachesCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowCheckWorkflow)

	chainBuilderClose(t, rt, "shop", strings.TrimSuffix(chainDoneBody(), "```\n"))

	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" {
		t.Errorf("awaiting = %+v (status %s, %q), want the check step", st.Awaiting, c.Status, c.Reason)
	}
	if c.Status == string(workflow.StatusHalted) {
		t.Errorf("chain halted: %q", c.Reason)
	}
}

// TestWorkflowDirectFixCustomActorsReachTheirSeeds pins the shipped workflow
// with actors not named after their parts, the way a repository with its own
// agents runs it: the fix-direct seed must still name the reviewer's output,
// and the correct step's plan must still reach the builder.
func TestWorkflowDirectFixCustomActorsReachTheirSeeds(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rows := chainRows()
	rows["team-reviewer"] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
	}
	rows["team-planner"] = rows["lite-planner"]
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	startedChain(t, rt, ChainOptions{Workflow: "direct-fix", ReviewerActor: "team-reviewer", PlannerActor: "team-planner"})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	row := flowChainRow(t, rt)
	if row.Reviewer != "shop-rev" || row.Planner != "shop-plan" {
		t.Fatalf("chain columns reviewer=%q planner=%q, want shop-rev and shop-plan", row.Reviewer, row.Planner)
	}
	builder := chainBinding(t, rt, "shop")
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", builder.Round))
	if err != nil {
		t.Fatalf("read the fix-direct prompt: %v", err)
	}
	rev := chainBinding(t, rt, "shop-rev")
	output := seedCopy(t, rt, rt.Store.OutputPath("shop-rev", 1, rev.WriterRole(), readerOutputLabel(rt, rev)))
	if !strings.Contains(string(prompt), "Reviewer's output: "+output+".") {
		t.Errorf("fix-direct prompt does not name the reviewer's output %s:\n%s", output, prompt)
	}
}

// customReviewerWorkflow is a file workflow whose actors are not named after
// their parts: a team-reviewer reader reviews, the builder fixes directly,
// and a team-planner corrects, all through the shipped seeds.
const customReviewerWorkflow = `name: custom-review
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: review } }
  review: { run: team-reviewer, seed: shipped:review, on: { verdict=pass: plans, verdict=changes: fix-direct } }
  fix-direct: { run: builder, seed: shipped:fix-direct, budget: { max: 1, per: plans, then: correct }, on: { done: review } }
  correct: { run: team-planner, seed: shipped:correct, on: { done: rebuild } }
  rebuild: { run: builder, seed: "{{correct.plan}}", on: { done: review } }
`

// TestSavedWorkflowCustomReviewerReachesItsSeeds pins the saved-workflow
// reviewer lookup: with no legacy columns, the fix-direct and correct seeds
// still name the reviewer's output, resolved through the workflow's review
// step and the chain's member rows.
func TestSavedWorkflowCustomReviewerReachesItsSeeds(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rows := chainRows()
	rows["team-reviewer"] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
	}
	rows["team-planner"] = rows["lite-planner"]
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	startFlowChain(t, rt, customReviewerWorkflow)

	row := flowChainRow(t, rt)
	if row.Reviewer != "" || row.Planner != "" {
		t.Fatalf("chain columns reviewer=%q planner=%q, want both empty on a saved workflow", row.Reviewer, row.Planner)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-team-reviewer", chainVerdictBody("changes"))

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want the fix-direct round 2", builder.Round)
	}
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", builder.Round))
	if err != nil {
		t.Fatalf("read the fix-direct prompt: %v", err)
	}
	rev := chainBinding(t, rt, "shop-team-reviewer")
	output := seedCopy(t, rt, rt.Store.OutputPath("shop-team-reviewer", 1, rev.WriterRole(), readerOutputLabel(rt, rev)))
	if !strings.Contains(string(prompt), "Reviewer's output: "+output+".") {
		t.Errorf("fix-direct prompt does not name the reviewer's output %s:\n%s", output, prompt)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-team-reviewer", chainVerdictBody("changes"))

	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "correct" {
		t.Fatalf("awaiting = %+v, want the correct step after two changes on one plan", st.Awaiting)
	}
	planner := chainBinding(t, rt, "shop-team-planner")
	if planner.Round != 1 {
		t.Fatalf("planner round = %d, want the correct round 1", planner.Round)
	}
	correctPrompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-team-planner", planner.Round))
	if err != nil {
		t.Fatalf("read the correct prompt: %v", err)
	}
	rev2 := chainBinding(t, rt, "shop-team-reviewer")
	output2 := seedCopy(t, rt, rt.Store.OutputPath("shop-team-reviewer", 2, rev2.WriterRole(), readerOutputLabel(rt, rev2)))
	if !strings.Contains(string(correctPrompt), "Reviewer's output: "+output2+".") {
		t.Errorf("correct prompt does not name the reviewer's output %s:\n%s", output2, correctPrompt)
	}
}

// customSecurityWorkflow is a file workflow whose actors are not named after
// their parts: a team-security reader scans, a team-planner fixes the
// findings, and a team-reviewer judges the fix round, all through the shipped
// seeds. Its step ids keep the shipped fix phase's names.
const customSecurityWorkflow = `name: custom-scan
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: scan } }
  scan: { run: team-security, seed: shipped:scan, on: { findings=0: done, findings>0: fix } }
  fix: { run: team-planner, seed: shipped:fix, on: { done: fix-build } }
  fix-build: { run: builder, seed: "{{fix.plan}}", on: { done: fix-review } }
  fix-review: { run: team-reviewer, seed: shipped:review, on: { verdict=pass: done, verdict=changes: { halt: "changes" } } }
`

// customSecurityRows is the roles file behind customSecurityWorkflow: the
// custom actors stand in for the shipped security, planner and reviewer.
func customSecurityRows(t *testing.T, rt Runtime) *roles.Registry {
	t.Helper()
	rows := chainRows()
	rows["team-security"] = rows["security"]
	rows["team-planner"] = rows["lite-planner"]
	rows["team-reviewer"] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
	}
	return rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
}

// TestSavedWorkflowCustomSecurityReachesTheFixSeed pins the saved-workflow
// security lookup: with no legacy columns, the fix seed still names the
// scanner's own output, resolved through the workflow's scan step and the
// chain's member rows.
func TestSavedWorkflowCustomSecurityReachesTheFixSeed(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rt.Registry = customSecurityRows(t, rt)
	startFlowChain(t, rt, customSecurityWorkflow)

	row := flowChainRow(t, rt)
	if row.Security != "" || row.Planner != "" {
		t.Fatalf("chain columns security=%q planner=%q, want both empty on a saved workflow", row.Security, row.Planner)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-team-security", chainFindingsBody(2))

	planner := chainBinding(t, rt, "shop-team-planner")
	if planner.Round != 1 {
		t.Fatalf("planner round = %d, want the fix round 1", planner.Round)
	}
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-team-planner", planner.Round))
	if err != nil {
		t.Fatalf("read the fix prompt: %v", err)
	}
	sec := chainBinding(t, rt, "shop-team-security")
	output := seedCopy(t, rt, rt.Store.OutputPath("shop-team-security", 1, sec.WriterRole(), readerOutputLabel(rt, sec)))
	if !strings.Contains(string(prompt), "Security output: "+output+".") {
		t.Errorf("fix prompt does not name the security output %s:\n%s", output, prompt)
	}
}

// TestSavedWorkflowCustomPlannerRoundIsNotHumanSent pins the planner check on
// the saved workflow's fix step: the builder round that ran the custom
// planner's plan is named a fix-plan round, not a round a human sent, because
// the check reads the planner through the workflow and member rows instead of
// the empty legacy column.
func TestSavedWorkflowCustomPlannerRoundIsNotHumanSent(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rt.Registry = customSecurityRows(t, rt)
	startFlowChain(t, rt, customSecurityWorkflow)

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-team-security", chainFindingsBody(2))
	chainReaderClose(t, rt, "shop-team-planner", "# Fix plan\n\n1. Repair the finding.\n")

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want the fix-build round 2", builder.Round)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-team-reviewer")
	if rev.Round != 1 {
		t.Fatalf("reviewer round = %d, want the fix-review round 1", rev.Round)
	}
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-team-reviewer", rev.Round))
	if err != nil {
		t.Fatalf("read the fix-review prompt: %v", err)
	}
	if want := "This closing round is " + workflow.BuilderRoundFix + ", on top of round 1."; !strings.Contains(string(prompt), want) {
		t.Errorf("fix-review prompt does not name %q:\n%s", want, prompt)
	}
	if strings.Contains(string(prompt), workflow.BuilderRoundHuman) {
		t.Errorf("fix-review prompt calls the planner's round human-sent:\n%s", prompt)
	}
}

// twoReviewerWorkflow reviews with two differently named actors in turn, and
// the sorted step ids put the reviewer that passes first.
const twoReviewerWorkflow = `name: two-review
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: review-a } }
  review-a: { run: team-reviewer, seed: shipped:review, on: { verdict=pass: review-b, verdict=changes: correct } }
  review-b: { run: other-reviewer, seed: shipped:review, on: { verdict=pass: plans, verdict=changes: correct } }
  correct: { run: team-planner, seed: shipped:correct, budget: { max: 2, per: plans, then: { halt: "reviewers still want changes" } }, on: { done: rebuild } }
  rebuild: { run: builder, seed: "{{correct.plan}}", on: { done: review-a } }
`

// TestSavedWorkflowCorrectionAnswersTheReviewThatAskedForIt pins the choice
// among reviewers: both are on their first round when the second asks for
// changes, so round numbers tie and only the time of each report finds it.
func TestSavedWorkflowCorrectionAnswersTheReviewThatAskedForIt(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rows := chainRows()
	reader := roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
	}
	rows["team-reviewer"] = reader
	rows["other-reviewer"] = reader
	rows["team-planner"] = rows["lite-planner"]
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	startFlowChain(t, rt, twoReviewerWorkflow)

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-team-reviewer", chainVerdictBody("pass"))
	chainReaderClose(t, rt, "shop-other-reviewer", chainVerdictBody("changes"))

	planner := chainBinding(t, rt, "shop-team-planner")
	if planner.Round != 1 {
		t.Fatalf("planner round = %d, want the correct round 1", planner.Round)
	}
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-team-planner", planner.Round))
	if err != nil {
		t.Fatalf("read the correct prompt: %v", err)
	}
	other := chainBinding(t, rt, "shop-other-reviewer")
	output := seedCopy(t, rt, rt.Store.OutputPath("shop-other-reviewer", 1, other.WriterRole(), readerOutputLabel(rt, other)))
	if !strings.Contains(string(prompt), "Reviewer's output: "+output+".") {
		t.Errorf("correct prompt does not name the reviewer that asked for changes, %s:\n%s", output, prompt)
	}
}

// TestSavedWorkflowCustomPlannerCorrectionIsNotHumanSent pins the planner check
// on the correct step, as TestSavedWorkflowCustomPlannerRoundIsNotHumanSent does
// on the fix step: the builder round that ran the plan is named a correction.
func TestSavedWorkflowCustomPlannerCorrectionIsNotHumanSent(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rows := chainRows()
	rows["team-reviewer"] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
	}
	rows["other-reviewer"] = rows["team-reviewer"]
	rows["team-planner"] = rows["lite-planner"]
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	startFlowChain(t, rt, twoReviewerWorkflow)

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-team-reviewer", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-team-planner", "# Correction\n\n1. Repair the finding.\n")
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-team-reviewer")
	if rev.Round != 2 {
		t.Fatalf("reviewer round = %d, want the re-review round 2", rev.Round)
	}
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-team-reviewer", rev.Round))
	if err != nil {
		t.Fatalf("read the re-review prompt: %v", err)
	}
	if want := "This closing round is " + workflow.BuilderRoundCorrection + ", on top of round 1."; !strings.Contains(string(prompt), want) {
		t.Errorf("re-review prompt does not name %q:\n%s", want, prompt)
	}
}

// TestChainSeedStepBindingPrefersASetColumn pins the two ends of the lookup: a
// set legacy column names the member without consulting the steps, and no
// matching step names none, which a seed words as a missing input.
func TestChainSeedStepBindingPrefersASetColumn(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rows := chainRows()
	rows["team-reviewer"] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
	}
	rows["other-reviewer"] = rows["team-reviewer"]
	rows["team-planner"] = rows["lite-planner"]
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	startFlowChain(t, rt, twoReviewerWorkflow)
	row := flowChainRow(t, rt)
	def, err := chain.WorkflowDef(row)
	if err != nil {
		t.Fatal(err)
	}
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		if got := chainSeedStepBinding(tx, row, def, "legacy-member", "shipped:review"); got != "legacy-member" {
			t.Errorf("with a set column = %q, want legacy-member", got)
		}
		if got := chainSeedStepBinding(tx, row, def, "", "shipped:scan"); got != "" {
			t.Errorf("with no such step = %q, want none", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
