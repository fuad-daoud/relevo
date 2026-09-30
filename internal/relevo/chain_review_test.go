package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/git"
)

// chainSourceBody is a builder report tail with only the fields a
// BuilderHaltReason source test varies. An empty field is left empty rather
// than quoted, so the source is genuinely absent.
func chainSourceBody(status, haltedAt, notDone string) string {
	body := "I stopped.\n\n```relevo\nstatus: " + status + "\n"
	if haltedAt != "" {
		body += "halted_at: \"" + haltedAt + "\"\n"
	} else {
		body += "halted_at: \"\"\n"
	}
	body += "changed_paths: []\ncommands_run: []\nnot_done: [" + notDone + "]\n```\n"
	return body
}

// TestChainBuilderHaltCarriesTheReportTailReason pins the whole halt path: a
// builder body whose tail names where it stopped reaches the chain row, the
// trace row, the encoded event and the one end delivery with the labelled
// reason, not the empty note the wiring used to format.
func TestChainBuilderHaltCarriesTheReportTailReason(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainHaltedBody("waiting on a decision"))

	want := "builder halted on plan 1: halted_at: waiting on a decision"
	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if row.Reason != want {
		t.Errorf("chain reason = %q, want %q", row.Reason, want)
	}

	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want the halt's one row", events)
	}
	if events[0].Reason != want {
		t.Errorf("trace reason = %q, want %q", events[0].Reason, want)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Reason != "halted_at: waiting on a decision" {
		t.Errorf("event reason = %q, want the report tail's source", ev.Reason)
	}

	pending := chainPendingChain(t, rt, "shop")
	if len(pending) != 1 {
		t.Fatalf("pending chain deliveries = %d, want 1", len(pending))
	}
	if !strings.Contains(pending[0].Payload, want) {
		t.Errorf("end payload = %q, want it to carry %q", pending[0].Payload, want)
	}
}

// TestChainBuilderHaltReasonSources pins the three remaining sources end to
// end: the close note, the first not_done item, and the outcome word when the
// tail is empty.
func TestChainBuilderHaltReasonSources(t *testing.T) {
	t.Parallel()

	t.Run("the note", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		// A marker with no report closes with the note "noreport": the tail
		// parses to nothing, so the note is the only source left.
		b := chainBinding(t, rt, "shop")
		touch(t, rt.Store.DonePath("shop", b.Round))
		chainReconcile(t, rt, "shop")

		if got := chainStoredRow(t, rt, "shop").Reason; got != "builder halted on plan 1: note: noreport" {
			t.Errorf("chain reason = %q, want the noreport note", got)
		}
	})

	t.Run("the first not_done item", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		chainBuilderClose(t, rt, "shop", chainSourceBody("halted", "", "finish the tests, then the docs"))

		if got := chainStoredRow(t, rt, "shop").Reason; got != "builder halted on plan 1: not_done: finish the tests" {
			t.Errorf("chain reason = %q, want the first not_done item", got)
		}
	})

	t.Run("the outcome when the tail is empty", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		chainBuilderClose(t, rt, "shop", chainSourceBody("blocked", "", ""))

		if got := chainStoredRow(t, rt, "shop").Reason; got != "builder halted on plan 1: status: blocked" {
			t.Errorf("chain reason = %q, want the outcome word", got)
		}
	})
}

// TestChainStartRecordsThePlanStartCommit pins plan 1's start: the chain row
// carries the commit its worktree was cut from.
func TestChainStartRecordsThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	res := startedChain(t, rt, ChainOptions{})

	if fg.headCommitID == "" {
		t.Fatal("test premise: the fake git must report a head commit")
	}
	if res.Chain.PlanStartCommit != fg.headCommitID {
		t.Errorf("result plan start = %q, want the cut commit %q", res.Chain.PlanStartCommit, fg.headCommitID)
	}
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != fg.headCommitID {
		t.Errorf("stored plan start = %q, want %q", got, fg.headCommitID)
	}
}

// chainPlanDiffFixtures arms the fake git so a round close produces both a
// round patch and a cumulative plan patch.
func chainPlanDiffFixtures(fg *fakeGit) {
	fg.snapshotTreeID = "tree-end"
	fg.diffResult = git.Diff{
		Stat:  git.Stat{FilesChanged: 2, Insertions: 5, Deletions: 1},
		Patch: []byte("--- a/f\n+++ b/f\n"),
	}
}

// TestChainReviewerSeedNamesThePlanCumulativeDiff pins the seed's cumulative
// line: with a plan-start commit and a closing round whose tree is known, the
// staged reviewer prompt names both this round's diff and the plan's whole
// diff, and the patch is stored at the plan-diff key.
func TestChainReviewerSeedNamesThePlanCumulativeDiff(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	if got, want := string(text), "This round's diff: "+rt.Store.DiffPath("shop", 1); !strings.Contains(got, want) {
		t.Errorf("reviewer seed does not name %q:\n%s", want, got)
	}
	if got, want := string(text), "Plan diff, every round of this plan so far: "+rt.Store.PlanDiffPath("shop", 1); !strings.Contains(got, want) {
		t.Errorf("reviewer seed does not name %q:\n%s", want, got)
	}

	patch, err := rt.Store.ReadFile(rt.Store.PlanDiffPath("shop", 1))
	if err != nil {
		t.Fatalf("read %s: %v", rt.Store.PlanDiffPath("shop", 1), err)
	}
	if string(patch) != "--- a/f\n+++ b/f\n" {
		t.Errorf("plan diff patch = %q, want the diff's patch", patch)
	}
}

// TestChainPlanDiffStartsAtThePlanStartCommit pins the span's start: a
// two-plan chain whose head moves between sends diffs plan 2's review from the
// head recorded at plan 2's send, not from the plan's closing round.
func TestChainPlanDiffStartsAtThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{Plans: []string{writePlan(t, "plan one"), writePlan(t, "plan two")}})

	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "commit-head-123" {
		t.Fatalf("plan 1 start = %q, want the cut commit", got)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	// The head moves before plan 2's send, so a review that diffed from the
	// closing round's baseline would name a different start.
	fg.headCommitID = "head-plan2"
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	row := chainStoredRow(t, rt, "shop")
	if row.Plan != 2 {
		t.Fatalf("chain plan = %d, want 2", row.Plan)
	}
	if row.PlanStartCommit != "head-plan2" {
		t.Fatalf("plan 2 start = %q, want the head recorded at plan 2's send", row.PlanStartCommit)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	if fg.lastDiffFrom != "head-plan2" {
		t.Errorf("the cumulative diff ran from %q, want plan 2's start %q", fg.lastDiffFrom, "head-plan2")
	}
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-plan2" {
		t.Errorf("plan start after the plan's review = %q, want it held at %q", got, "head-plan2")
	}
}

// TestChainPlanStartCommitResetOnlyOnNewPlans pins which sends record the plan
// start: a correction keeps it, a reviewer's pass onto the next plan moves it
// to that send's baseline head, and a security fix-plan send moves it too.
func TestChainPlanStartCommitResetOnlyOnNewPlans(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{
		Plans:          []string{writePlan(t, "plan one"), writePlan(t, "plan two")},
		MaxCorrections: ptr(2),
		Security:       ptr(true),
	})

	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "commit-head-123" {
		t.Fatalf("plan 1 start = %q, want the cut commit", got)
	}

	// A correction is sent by the planner's close. It must leave the recorded
	// start alone even though the head has moved.
	fg.headCommitID = "head-correction"
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "commit-head-123" {
		t.Fatalf("plan start after a correction = %q, want it held at the plan 1 start", got)
	}

	// The reviewer's pass onto plan 2 moves it to that send's baseline head.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	fg.headCommitID = "head-plan2"
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-plan2" {
		t.Fatalf("plan start after the pass = %q, want plan 2's head", got)
	}

	// A reviewer send into the security phase changes nothing.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-plan2" {
		t.Fatalf("plan start after the security send = %q, want plan 2's head", got)
	}

	// The security phase's fix plan moves it to that send's baseline head.
	chainReaderClose(t, rt, "shop-sec", chainFindingsBody(1))
	fg.headCommitID = "head-fix"
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-fix" {
		t.Errorf("plan start after the fix plan = %q, want %q", got, "head-fix")
	}
}

// TestChainCorrectionRoundRunsThePlannersPlan pins the correction delivery: a
// correction planner's own plan is what the builder's next round is handed, not
// the chain's copy of the plan.
func TestChainCorrectionRoundRunsThePlannersPlan(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	const planText = "# Correction plan\n\n1. Make the change the reviewer asked for.\n"
	chainReaderClose(t, rt, "shop-plan", planText)

	builder := chainBinding(t, rt, "shop")
	staged, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", builder.Round))
	if err != nil {
		t.Fatalf("read the builder's staged prompt: %v", err)
	}
	if string(staged) != planText {
		t.Errorf("builder round %d prompt = %q, want the planner's own plan %q", builder.Round, staged, planText)
	}
	copied, err := rt.Store.ReadFile(rt.Store.ChainPlanPath("shop", 1))
	if err != nil {
		t.Fatalf("read the chain's plan copy: %v", err)
	}
	if string(staged) == string(copied) {
		t.Errorf("the builder was handed the plan copy %q, not the planner's plan", copied)
	}
}

// TestChainReviewerSeedNamesTheRoundPromptOfACorrection pins the round prompt:
// a reviewer seed after a round that ran a planner's plan names that round's
// staged prompt.
func TestChainReviewerSeedNamesTheRoundPromptOfACorrection(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", "# Correction plan\n\nDo it.\n")
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	if want := "This round's prompt: " + rt.Store.PromptPath("shop", 2); !strings.Contains(string(text), want) {
		t.Errorf("reviewer seed does not name %q:\n%s", want, text)
	}
}

// TestChainReviewerSeedOmitsThePlanCopyAsRoundPrompt pins the plan's own round:
// when the round ran the plan copy itself, the seed names no round prompt.
func TestChainReviewerSeedOmitsThePlanCopyAsRoundPrompt(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	if strings.Contains(string(text), "This round's prompt:") {
		t.Errorf("plan 1's own round names a round prompt:\n%s", text)
	}
}

// TestChainCorrectionSeedNamesTheJudgedBuilderRound pins the manual-round
// resume: after a resume reviews a newer manual builder round and the reviewer
// asks for changes, the correction seed names that builder round -- its report,
// diff, prompt and cumulative diff -- not the reviewer's own round.
func TestChainCorrectionSeedNamesTheJudgedBuilderRound(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	stoppedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	// A manual round while the chain is stopped: it closes without a chain
	// transition, so its record is the one a later resume reviews.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the stop: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	builderRound := chainBinding(t, rt, "shop").Round - 1
	if builderRound != 2 {
		t.Fatalf("manual builder round = %d, want 2", builderRound)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	planner := chainBinding(t, rt, "shop-plan")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-plan", planner.Round))
	if err != nil {
		t.Fatalf("read the correction prompt: %v", err)
	}
	got := string(text)
	for _, want := range []string{
		rt.Store.ReportPath("shop", builderRound),
		"This round's diff: " + rt.Store.DiffPath("shop", builderRound),
		"This round's prompt: " + rt.Store.PromptPath("shop", builderRound),
		"Plan diff, every round of this plan so far: " + rt.Store.PlanDiffPath("shop", builderRound),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("correction seed does not name %q:\n%s", want, got)
		}
	}
}

// TestChainResumeReviewKeepsThePlanStartCommit pins the resume's start: a
// manual round's review diffs from the commit the chain stored, to the manual
// round's own closed tree, even though the head moved before the resume.
func TestChainResumeReviewKeepsThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	stoppedChain(t, rt, ChainOptions{})
	stored := chainStoredRow(t, rt, "shop").PlanStartCommit
	if stored == "" {
		t.Fatal("test premise: the chain must record a plan-start commit")
	}

	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the stop: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	// A moved head must not be picked up: the stored commit is the plan start.
	fg.headCommitID = "head-moved"
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	if fg.lastDiffFrom != stored {
		t.Errorf("the cumulative diff ran from %q, want the stored plan start %q", fg.lastDiffFrom, stored)
	}
	if fg.lastDiffTo != "tree-end" {
		t.Errorf("the cumulative diff ran to %q, want the manual round's closed tree %q", fg.lastDiffTo, "tree-end")
	}
}

// TestChainResumeReSendKeepsThePlanStartCommit pins the re-send half: resuming
// a stopped build round hands the plan to the builder again and leaves the
// recorded start untouched.
func TestChainResumeReSendKeepsThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})
	before := chainStoredRow(t, rt, "shop").PlanStartCommit
	if before == "" {
		t.Fatal("test premise: the chain must record a plan-start commit")
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepBuilding) {
		t.Fatalf("step = %q, want the building step re-sent", row.Step)
	}
	if row.PlanStartCommit != before {
		t.Errorf("plan start after a re-send = %q, want it held at %q", row.PlanStartCommit, before)
	}
}
