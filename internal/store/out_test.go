package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestReportPathPrefersOut pins the total resolver: out/ wins whenever it
// exists (on disk or as a sealed row), the old flat path answers only when it
// alone exists, and a fresh round is named in out/. DonePath resolves the same
// way.
func TestReportPathPrefersOut(t *testing.T) {
	t.Parallel()

	s, name := seedBinding(t)
	outReport := filepath.Join(s.OutDir(name), "001-report.md")
	oldReport := filepath.Join(s.Dir(name), "001-report.md")
	outDone := filepath.Join(s.OutDir(name), "001-done")

	if got := s.ReportPath(name, 1); got != outReport {
		t.Errorf("fresh ReportPath = %q, want the out/ name %q", got, outReport)
	}
	if got := s.DonePath(name, 1); got != outDone {
		t.Errorf("fresh DonePath = %q, want the out/ name %q", got, outDone)
	}

	// Old-only on disk falls back to the old path.
	if err := os.WriteFile(oldReport, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.ReportPath(name, 1); got != oldReport {
		t.Errorf("old-only ReportPath = %q, want %q", got, oldReport)
	}
	oldDone := filepath.Join(s.Dir(name), "001-done")
	if err := os.WriteFile(oldDone, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.DonePath(name, 1); got != oldDone {
		t.Errorf("old-only DonePath = %q, want %q", got, oldDone)
	}

	// Both present: out/ wins.
	if err := os.MkdirAll(s.OutDir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outReport, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.ReportPath(name, 1); got != outReport {
		t.Errorf("both-homes ReportPath = %q, want out/ %q", got, outReport)
	}

	// A sealed row, with nothing on disk, still resolves to out/.
	if err := os.Remove(outReport); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldReport); err != nil {
		t.Fatal(err)
	}
	if err := s.WithLock(func(tx *Tx) error {
		return tx.PutRoundFile(name, 1, outReport, []byte("sealed"))
	}); err != nil {
		t.Fatalf("PutRoundFile: %v", err)
	}
	if got := s.ReportPath(name, 1); got != outReport {
		t.Errorf("sealed ReportPath = %q, want out/ %q", got, outReport)
	}
}

// TestArtifactDirPreferOut pins the same resolution for a round's artifact
// directory.
func TestArtifactDirPreferOut(t *testing.T) {
	t.Parallel()

	s, name := seedBinding(t)
	outDir := filepath.Join(s.OutDir(name), "001-reviewer")
	oldDir := filepath.Join(s.Dir(name), "001-reviewer")

	if got := s.ArtifactDir(name, 1, "reviewer"); got != outDir {
		t.Errorf("fresh ArtifactDir = %q, want out/ %q", got, outDir)
	}
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := s.ArtifactDir(name, 1, "reviewer"); got != oldDir {
		t.Errorf("old-only ArtifactDir = %q, want %q", got, oldDir)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := s.ArtifactDir(name, 1, "reviewer"); got != outDir {
		t.Errorf("both-homes ArtifactDir = %q, want out/ %q", got, outDir)
	}
}

// TestEnsureOutDir pins that it creates out/ and refuses a symlinked one.
func TestEnsureOutDir(t *testing.T) {
	t.Parallel()

	s, name := seedBinding(t)
	if err := s.EnsureOutDir(name); err != nil {
		t.Fatalf("EnsureOutDir: %v", err)
	}
	fi, err := os.Lstat(s.OutDir(name))
	if err != nil || !fi.IsDir() {
		t.Fatalf("out dir = %v, %v; want a directory", fi, err)
	}

	// A symlink at out/ is refused, never followed.
	s2, name2 := seedBinding(t)
	target := t.TempDir()
	if err := os.Symlink(target, s2.OutDir(name2)); err != nil {
		t.Fatal(err)
	}
	if err := s2.EnsureOutDir(name2); err == nil {
		t.Fatalf("EnsureOutDir on a symlinked out/ = nil error, want a refusal")
	}
}

// TestMigrateOutLayout pins the migration: report, done marker and artifact
// directory move into out/ with identical bytes, a destination that already
// exists is never clobbered, and a second run moves nothing.
func TestMigrateOutLayout(t *testing.T) {
	t.Parallel()

	s, name := seedBinding(t)
	dir := s.Dir(name)
	if err := os.WriteFile(filepath.Join(dir, "001-report.md"), []byte("report-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-done"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "001-reviewer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-reviewer", "summary.md"), []byte("artifact-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stream and a prompt are not runner outputs and must stay put.
	if err := os.WriteFile(filepath.Join(dir, "001-runner.jsonl"), []byte("stream"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := s.MigrateOutLayout(name)
	if err != nil {
		t.Fatalf("MigrateOutLayout: %v", err)
	}
	if moved != 3 {
		t.Errorf("moved = %d, want 3", moved)
	}

	for _, tc := range []struct {
		rel  string
		body string
	}{
		{"001-report.md", "report-body"},
		{"001-reviewer/summary.md", "artifact-body"},
	} {
		got, err := os.ReadFile(filepath.Join(s.OutDir(name), filepath.FromSlash(tc.rel)))
		if err != nil || string(got) != tc.body {
			t.Errorf("out/%s = %q, %v; want %q", tc.rel, got, err, tc.body)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "001-report.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old report still present: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "001-reviewer")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old artifact dir still present: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "001-runner.jsonl")); err != nil {
		t.Errorf("stream moved: %v", err)
	}

	if moved, err := s.MigrateOutLayout(name); err != nil || moved != 0 {
		t.Errorf("second MigrateOutLayout = %d, %v; want 0, nil", moved, err)
	}
}

// TestMigrateOutLayoutLeavesBothHomes pins that a destination that already
// exists is never clobbered: the old file is left untouched and the out/ file
// is not replaced.
func TestMigrateOutLayoutLeavesBothHomes(t *testing.T) {
	t.Parallel()

	s, name := seedBinding(t)
	dir := s.Dir(name)
	if err := os.MkdirAll(s.OutDir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "002-report.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.OutDir(name), "002-report.md"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, err := s.MigrateOutLayout(name)
	if err != nil {
		t.Fatalf("MigrateOutLayout (both homes): %v", err)
	}
	if moved != 0 {
		t.Errorf("both-homes moved = %d, want 0", moved)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "002-report.md")); err != nil || string(got) != "old" {
		t.Errorf("old file = %q, %v; want it left untouched", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(s.OutDir(name), "002-report.md")); err != nil || string(got) != "new" {
		t.Errorf("out file = %q, %v; want it untouched", got, err)
	}
}

// TestReadRunnerOutputRefusesSymlinks pins that a symlink at a runner-output
// name -- a flat report and a nested artifact -- is refused by the read and its
// target is never touched.
func TestReadRunnerOutputRefusesSymlinks(t *testing.T) {
	t.Parallel()

	s, name := seedBinding(t)
	if err := os.MkdirAll(filepath.Join(s.OutDir(name), "001-reviewer"), 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}

	reportPath := filepath.Join(s.OutDir(name), "001-report.md")
	if err := os.Symlink(sentinel, reportPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(reportPath); err == nil {
		t.Errorf("ReadFile on a symlinked report = nil error, want a refusal")
	}
	if _, _, _, err := s.StatFile(reportPath); err == nil {
		t.Errorf("StatFile on a symlinked report = nil error, want a refusal")
	}

	artifactPath := filepath.Join(s.OutDir(name), "001-reviewer", "summary.md")
	if err := os.Symlink(sentinel, artifactPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(artifactPath); err == nil {
		t.Errorf("ReadFile on a symlinked artifact = nil error, want a refusal")
	}

	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "sentinel" {
		t.Errorf("sentinel = %q, %v; want it byte-identical", body, err)
	}
}

// TestSealedRunnerOutputReadsThroughNewPath pins that a row, not a file, still
// answers through the out/ name.
func TestSealedRunnerOutputReadsThroughNewPath(t *testing.T) {
	t.Parallel()

	s, name := seedBinding(t)
	outReport := filepath.Join(s.OutDir(name), "001-report.md")
	if err := s.WithLock(func(tx *Tx) error {
		return tx.PutRoundFile(name, 1, outReport, []byte("sealed-body"))
	}); err != nil {
		t.Fatalf("PutRoundFile: %v", err)
	}

	body, err := s.ReadFile(outReport)
	if err != nil || string(body) != "sealed-body" {
		t.Fatalf("ReadFile(sealed out report) = %q, %v; want sealed-body", body, err)
	}
	if size, _, ok, err := s.StatFile(outReport); err != nil || !ok || size != int64(len("sealed-body")) {
		t.Fatalf("StatFile(sealed out report) = %d, %v, %v; want %d, true, nil", size, ok, err, len("sealed-body"))
	}
}
