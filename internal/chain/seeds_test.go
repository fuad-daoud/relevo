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
		PlanPath:       "/tmp/chain/plan-2.md",
		ReportPath:     "/tmp/chain/report.md",
		DiffPath:       "/tmp/chain/round.diff",
		GateLogPath:    "/tmp/chain/gate.log",
		GateResult:     GateRed,
		OutputPath:     "/tmp/chain/review.md",
		BranchDiffPath: "/tmp/chain/branch.diff",
		Branch:         "relevo/x",
		Base:           "main",
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
		v.PlanPath, v.ReportPath, v.DiffPath, v.GateLogPath, v.GateResult, "```relevo", "verdict:")
}

func TestCorrectionSeedNamesTheReviewerOutput(t *testing.T) {
	t.Parallel()
	v := sampleSeedView()
	assertSeedNames(t, SeedCorrection, v, v.OutputPath, v.PlanPath, v.ReportPath, v.DiffPath)
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
