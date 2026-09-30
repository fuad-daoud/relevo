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
		PlanPath:        "/tmp/chain/plan-2.md",
		ReportPath:      "/tmp/chain/report.md",
		DiffPath:        "/tmp/chain/round.diff",
		GateLogPath:     "/tmp/chain/gate.log",
		GateResult:      GateRed,
		OutputPath:      "/tmp/chain/review.md",
		BranchDiffPath:  "/tmp/chain/branch.diff",
		PlanDiffPath:    "/tmp/chain/plan-diff.patch",
		RoundPromptPath: "/tmp/chain/round-prompt.md",
		Branch:          "relevo/x",
		Base:            "main",
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
	out := assertSeedNames(t, SeedReviewer, v, "No cumulative plan diff was captured for this plan.")
	if strings.Contains(out, "Plan diff, every round of this plan so far:") {
		t.Errorf("reviewer seed names a cumulative diff it does not have:\n%s", out)
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
