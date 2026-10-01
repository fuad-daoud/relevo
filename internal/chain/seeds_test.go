package chain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleSeedView() SeedView {
	return SeedView{
		Plan: 2, Plans: 4, Corrections: 1,
		PlanPath:         "/tmp/chain/plan-2.md",
		ReportPath:       "/tmp/chain/report.md",
		DiffPath:         "/tmp/chain/round.diff",
		GateLogPath:      "/tmp/chain/gate.log",
		GateResult:       GateRed,
		OutputPath:       "/tmp/chain/review.md",
		BranchDiffPath:   "/tmp/chain/branch.diff",
		PlanDiffPath:     "/tmp/chain/plan-diff.patch",
		RoundPromptPath:  "/tmp/chain/round-prompt.md",
		Branch:           "relevo/x",
		Base:             "main",
		BuilderRoundKind: BuilderRoundCorrection,
		BuilderRoundOn:   1,
		BuilderRounds: []SeedRound{
			{Round: 1, PromptPath: "/tmp/chain/round-1-prompt.md", ReportPath: "/tmp/chain/round-1-report.md"},
			{Round: 2, PromptPath: "/tmp/chain/round-2-prompt.md", ReportPath: "/tmp/chain/round-2-report.md"},
		},
		DiffFrom: "/tmp/chain/plan-start-commit",
	}
}

func assertSeedNames(t *testing.T, kind SeedKind, v SeedView, want ...string) string {
	t.Helper()
	out, err := Seed(kind, v)
	if err != nil {
		t.Fatalf("Seed(%s): %v", kind, err)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%s seed does not name %q:\n%s", kind, w, out)
		}
	}
	return out
}

func TestReviewerSeedNamesPlanReportDiffGateAndVerdictBlock(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	assertSeedNames(t, SeedReviewer, v,
		v.PlanPath, v.ReportPath, v.DiffPath, v.GateLogPath, v.GateResult, "Check result:", "```relevo", "verdict:")
}

// TestReviewerSeedNamesThePlanDiffAndTheRoundPrompt pins the two new inputs:
// when the view carries them the reviewer seed names both, each with its own
// label.
func TestReviewerSeedNamesThePlanDiffAndTheRoundPrompt(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	out := assertSeedNames(t, SeedReviewer, v,
		"This round's diff: "+v.DiffPath,
		"Plan diff, every round of this plan so far: "+v.PlanDiffPath,
		"This round's prompt: "+v.RoundPromptPath)
	if strings.Contains(out, "No cumulative plan diff") {
		t.Errorf("reviewer seed says there is no cumulative diff while a path is set:\n%s", out)
	}
}

// TestReviewerSeedOmitsAnUnsetPlanDiff pins the no-cumulative-diff rendering:
// an empty field names no path and leaves no dangling line.
func TestReviewerSeedOmitsAnUnsetPlanDiff(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	v.PlanDiffPath = ""
	// With no commit to diff from either, the seed words the plain miss.
	v.DiffFrom = ""
	out := assertSeedNames(t, SeedReviewer, v, "No cumulative plan diff was captured for this plan.")
	if strings.Contains(out, "Plan diff, every round of this plan so far:") {
		t.Errorf("reviewer seed names a cumulative diff it does not have:\n%s", out)
	}
}

// TestReviewerSeedFramesThePlanAndListsTheRounds pins the plan framing and the
// closing round's kind: the reviewer judges the plan as a whole, the closing
// round is named with the round it sits on top of, and every builder round of
// the plan is listed with both of its paths.
func TestReviewerSeedFramesThePlanAndListsTheRounds(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	out := assertSeedNames(t, SeedReviewer, v,
		"as a whole",
		"the plan's cumulative diff is the primary input",
		"this round's diff is its latest increment",
		"This closing round is "+v.BuilderRoundKind+", on top of round 1.",
		"Builder rounds of this plan:",
		"- round 1 prompt: "+v.BuilderRounds[0].PromptPath,
		"- round 1 report: "+v.BuilderRounds[0].ReportPath,
		"- round 2 prompt: "+v.BuilderRounds[1].PromptPath,
		"- round 2 report: "+v.BuilderRounds[1].ReportPath,
	)
	if strings.Contains(out, "judge the diff against the plan") {
		t.Errorf("reviewer seed still judges the diff rather than the plan:\n%s", out)
	}
}

// TestReviewerSeedNamesTheDiffFromWithoutAPlanDiff pins the diff-from-the-commit
// rendering: with no cumulative diff captured but a commit to diff from, the
// seed names that commit, says no cumulative diff exists, and names no
// cumulative-diff line.
func TestReviewerSeedNamesTheDiffFromWithoutAPlanDiff(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	v.PlanDiffPath = ""
	out := assertSeedNames(t, SeedReviewer, v,
		"No cumulative plan diff was captured; diff the plan yourself from "+v.DiffFrom+".",
		"This round's diff: "+v.DiffPath)
	if strings.Contains(out, "Plan diff, every round of this plan so far:") {
		t.Errorf("reviewer seed names a cumulative diff it does not have:\n%s", out)
	}
	if strings.Contains(out, "No cumulative plan diff was captured for this plan.") {
		t.Errorf("reviewer seed names no commit to diff from while DiffFrom is set:\n%s", out)
	}
}

// TestCorrectionSeedFramesThePlan pins the correction template's plan framing:
// it carries the same whole-plan framing and round list the reviewer's does.
func TestCorrectionSeedFramesThePlan(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	out := assertSeedNames(t, SeedCorrection, v,
		"as a whole",
		"the plan's cumulative diff is the primary input",
		"This closing round is "+v.BuilderRoundKind+", on top of round 1.",
		"- round 1 prompt: "+v.BuilderRounds[0].PromptPath,
		"- round 2 report: "+v.BuilderRounds[1].ReportPath,
	)
	if strings.Contains(out, "judge the diff against the plan") {
		t.Errorf("correction seed still judges the diff rather than the plan:\n%s", out)
	}
}

// TestReviewerSeedOmitsAnUnsetRoundPrompt pins the plan-copy case: an empty
// RoundPromptPath names no round prompt at all.
func TestReviewerSeedOmitsAnUnsetRoundPrompt(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	v.RoundPromptPath = ""
	out := assertSeedNames(t, SeedReviewer, v, v.PlanPath)
	if strings.Contains(out, "This round's prompt:") {
		t.Errorf("reviewer seed names a round prompt it does not have:\n%s", out)
	}
}

func TestCorrectionSeedNamesTheReviewerOutput(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	assertSeedNames(t, SeedCorrection, v, v.OutputPath, v.PlanPath, v.ReportPath, v.DiffPath, "Check result:")
}

// TestCorrectionSeedNamesThePlanDiffAndTheRoundPrompt pins the correction
// template's two new inputs, the same shape the reviewer's carries.
func TestCorrectionSeedNamesThePlanDiffAndTheRoundPrompt(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	assertSeedNames(t, SeedCorrection, v,
		"This round's diff: "+v.DiffPath,
		"Plan diff, every round of this plan so far: "+v.PlanDiffPath,
		"This round's prompt: "+v.RoundPromptPath)
}

// TestReviewerSeedSaysNoCheckRan pins the no-check rendering: an empty gate log
// path drops the check line for the exact sentence and names no log.
func TestReviewerSeedSaysNoCheckRan(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	v.GateLogPath, v.GateResult = "", ""
	out := assertSeedNames(t, SeedReviewer, v,
		"No check ran for this round.", v.PlanPath, v.ReportPath, v.DiffPath, "```relevo", "verdict:")
	if strings.Contains(out, "Check result:") {
		t.Errorf("reviewer seed with no check still says Check result:\n%s", out)
	}
}

// TestCorrectionSeedSaysNoCheckRan is the correction template's twin of the
// reviewer's no-check rendering.
func TestCorrectionSeedSaysNoCheckRan(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	v.GateLogPath, v.GateResult = "", ""
	out := assertSeedNames(t, SeedCorrection, v,
		"No check ran for this round.", v.OutputPath, v.PlanPath, v.ReportPath, v.DiffPath)
	if strings.Contains(out, "Check result:") {
		t.Errorf("correction seed with no check still says Check result:\n%s", out)
	}
}

func TestSecuritySeedNamesBranchDiffAndCountBlock(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	assertSeedNames(t, SeedSecurity, v, v.BranchDiffPath, v.Branch, v.Base, "```relevo", "findings:")
}

func TestFixesSeedNamesSecurityOutput(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	assertSeedNames(t, SeedFixes, v, v.OutputPath, v.BranchDiffPath)
}

func TestSeedRejectsAnUnknownKind(t *testing.T) {
	t.Parallel()
	if _, err := Seed(SeedKind("bogus"), sampleSeedView()); err == nil {
		t.Fatal("Seed(unknown kind): want an error")
	}
}

// TestSeedsNamePathsOnly pins that a seed is a prompt about files, never a copy
// of them: every seed succeeds with a path it never reads, names that path, and
// does not carry the file's bytes.
func TestSeedsNamePathsOnly(t *testing.T) {
	t.Parallel()
	const sentinel = "SEED-INLINE-SENTINEL"
	path := filepath.Join(t.TempDir(), "input.md")
	if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	v := SeedView{
		PlanPath: path, ReportPath: path, DiffPath: path, GateLogPath: path,
		OutputPath: path, BranchDiffPath: path,
		GateResult: GateGreen, Branch: "b", Base: "b",
	}
	for _, kind := range []SeedKind{SeedReviewer, SeedCorrection, SeedSecurity, SeedFixes} {
		out, err := Seed(kind, v)
		if err != nil {
			t.Fatalf("Seed(%s): %v", kind, err)
		}
		if !strings.Contains(out, path) {
			t.Fatalf("Seed(%s) does not name %s:\n%s", kind, path, out)
		}
		if strings.Contains(out, sentinel) {
			t.Fatalf("Seed(%s) inlined a file's content", kind)
		}
	}
}
