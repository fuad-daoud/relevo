package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// flowShippedReviewWorkflow is a one-plan workflow whose review step names the
// shipped review seed, so a send exercises the shipped template rendering.
const flowShippedReviewWorkflow = `name: shippedseed
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: review } }
  review: { run: assistant, seed: shipped:review, on: { verdict=pass: done, verdict=changes: done } }
`

// flowPlanDiffWorkflow is a one-plan workflow whose review step names the
// shipped review seed, so the plan's cumulative diff renders into the seed.
const flowPlanDiffWorkflow = `name: plandiff
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: review } }
  review: { run: assistant, seed: shipped:review, on: { verdict=pass: done, verdict=changes: done } }
`

// TestWorkflowSendRendersShippedSeed pins the engine's shipped seed: a review
// step's send carries the review template's text, not the literal name.
func TestWorkflowSendRendersShippedSeed(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowShippedReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-assistant", 1))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	text := string(prompt)
	if strings.Contains(text, "shipped:review") {
		t.Fatalf("the review send carried the literal seed name:\n%s", text)
	}
	if !strings.Contains(text, "Review the round and give a verdict.") {
		t.Fatalf("the review send does not carry the shipped review template:\n%s", text)
	}
}

// TestWorkflowReaderCloseRoutesOnVerdictNotSummaryOutcome pins the reader's
// status: a reader whose saved summary reads unstructured still routes on the
// verdict its own block carries, so a pass finishes the chain rather than
// halting on the summary's outcome.
func TestWorkflowReaderCloseRoutesOnVerdictNotSummaryOutcome(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	body := []byte("I reviewed the round.\n\n```relevo\nverdict: pass\n```\n")
	path := writeGapReaderOutput(t, body)
	b := chainBinding(t, rt, "shop-assistant")
	ev := closeWFEvent(t, rt, b, body, path, reporttail.OutcomeUnstructured)
	if ev.Status != reporttail.OutcomeDone {
		t.Fatalf("event status = %q, want done from the block, not the summary outcome %q", ev.Status, reporttail.OutcomeUnstructured)
	}
	if ev.Outcomes["verdict"] != "pass" {
		t.Fatalf("outcomes = %+v, want verdict=pass", ev.Outcomes)
	}
	flowAdvance(t, rt, ev)
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("row = %s %q, want done: a pass verdict must not halt on the summary outcome", got.Status, got.Reason)
	}
}

// TestWorkflowReaderHaltedStatusHalts pins the reader's own status word: a
// reader whose block declares outcomes and says halted closes the step halted.
func TestWorkflowReaderHaltedStatusHalts(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	body := []byte("I reviewed the round.\n\n```relevo\nstatus: halted\nverdict: pass\n```\n")
	path := writeGapReaderOutput(t, body)
	b := chainBinding(t, rt, "shop-assistant")
	ev := closeWFEvent(t, rt, b, body, path, reporttail.OutcomeDone)
	if ev.Status != reporttail.OutcomeHalted {
		t.Fatalf("event status = %q, want halted from the block's own status", ev.Status)
	}
	flowAdvance(t, rt, ev)
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusHalted) {
		t.Errorf("row = %s %q, want halted", got.Status, got.Reason)
	}
}

// TestWorkflowParseOutcomesReadsTheClosedRound pins the round the outcome parse
// reads: it is the round that closed, not the binding's own round, which the
// close has already advanced.
func TestWorkflowParseOutcomesReadsTheClosedRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	rev := chainBinding(t, rt, "shop-rev")
	info, ok := rt.RoleRegistry().ActorInfo(BindingRole(rev))
	if !ok {
		t.Fatalf("no actor info for reviewer")
	}

	stream := chainStreamResultLine(t, "```relevo\nverdict: changes\n```\n")
	if err := os.WriteFile(rt.Store.StreamPath("shop-rev", 1), []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	rev.Round = 2
	if err := rt.Store.Save(rev); err != nil {
		t.Fatalf("Save: %v", err)
	}

	vals, missing := chainParseOutcomes(rt, rev, 1, []byte("summary with no block\n"), info.Outputs)
	if missing != "" {
		t.Fatalf("parse of the closed round 1: %q, want verdict from round 1's stream", missing)
	}
	if vals["verdict"] != "changes" {
		t.Fatalf("verdict = %q, want changes from the closed round", vals["verdict"])
	}
	if _, missing := chainParseOutcomes(rt, rev, 2, []byte("summary with no block\n"), info.Outputs); missing == "" {
		t.Fatalf("parse of round 2 found an outcome; the binding's round 2 carries none")
	}
}

// TestWorkflowReviewerSeedNamesThePlanCumulativeDiff pins the engine's plan
// start: the send that opens a plan records the commit the plan began at, so
// the plan's review seed names the cumulative plan-diff copy.
func TestWorkflowReviewerSeedNamesThePlanCumulativeDiff(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startFlowChain(t, rt, flowPlanDiffWorkflow)

	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != fg.headCommitID {
		t.Fatalf("plan 1 start = %q, want the cut commit %q", got, fg.headCommitID)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	planDiffKey := rt.Store.PlanDiffPath("shop", 1)
	planDiffCopy, ok := rt.Store.ChainInputPath("shop", planDiffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", planDiffKey)
	}
	rev := chainBinding(t, rt, "shop-assistant")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-assistant", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	if want := "Plan diff, every round of this plan so far: " + planDiffCopy + "."; !strings.Contains(string(text), want) {
		t.Errorf("reviewer seed does not name the plan-diff copy %q:\n%s", want, text)
	}
	if patch, err := rt.Store.ReadFile(planDiffKey); err != nil || len(patch) == 0 {
		t.Errorf("plan diff at %s = %q (err %v), want the captured patch", planDiffKey, patch, err)
	}
}

// closeWFEvent builds the engine's close event for a reader from a body and its
// saved output path, under the store lock as the daemon does.
func closeWFEvent(t *testing.T, rt Runtime, b store.Binding, body []byte, path, outcome string) workflow.Event {
	t.Helper()
	var ev workflow.Event
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		ev, err = chainEventFromCloseWF(rt, tx, c, b, chainCloseWF{Body: body, Path: path, Outcome: outcome})
		return err
	}); err != nil {
		t.Fatalf("chainEventFromCloseWF: %v", err)
	}
	return ev
}

// writeGapReaderOutput writes a reader's output body to a temp file and returns
// its path, so the declared artifact is present.
func writeGapReaderOutput(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "findings.md")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write reader output: %v", err)
	}
	return path
}

// flowDiffSeedWorkflow is a workflow whose second step's seed names the closed
// build round's diff, so a wrong artifact key would render the next round's.
const flowDiffSeedWorkflow = `name: diffseed
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: use } }
  use: { run: assistant, seed: "the diff: {{build.diff}}", on: { verdict=pass: done, verdict=changes: done } }
`

// TestWorkflowCloseKeysTheClosedRoundsDiff pins the artifact's round: a build
// close has already advanced the binding to the next round, so the diff artifact
// it keys must be the round that closed. A custom workflow whose next seed names
// {{build.diff}} then resolves to that closed round's diff, not a path for a
// round that never captured one.
func TestWorkflowCloseKeysTheClosedRoundsDiff(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startFlowChain(t, rt, flowDiffSeedWorkflow)
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	key := rt.Store.DiffPath("shop", 1)
	copyPath, ok := rt.Store.ChainInputPath("shop", key)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", key)
	}
	assistant := chainBinding(t, rt, "shop-assistant")
	prompt, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-assistant", assistant.Round))
	if err != nil {
		t.Fatalf("read the use seed: %v", err)
	}
	if want := "the diff: " + copyPath; !strings.Contains(string(prompt), want) {
		t.Errorf("the use seed does not name the closed round's diff copy %q:\n%s", want, prompt)
	}
}
