package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestShowSectionFlagsConflict pins `relevo show`'s section-flag rules:
// none given defaults to plan, exactly one wins, more than one is a usage
// error. showSectionFlags is a pure function, so this never executes the
// subcommand -- CI launches no harness.
func TestShowSectionFlagsConflict(t *testing.T) {
	section, err := showSectionFlags(showSectionArgs{})
	if err != nil {
		t.Fatalf("no flags: err = %v, want nil", err)
	}
	if section != relevo.ShowPlan {
		t.Errorf("no flags: section = %q, want %q (default)", section, relevo.ShowPlan)
	}

	section, err = showSectionFlags(showSectionArgs{report: true})
	if err != nil {
		t.Fatalf("--report: err = %v, want nil", err)
	}
	if section != relevo.ShowReport {
		t.Errorf("--report: section = %q, want %q", section, relevo.ShowReport)
	}

	if _, err := showSectionFlags(showSectionArgs{plan: true, report: true}); err == nil {
		t.Error("--plan --report: err = nil, want a usage error (more than one section)")
	}
}

// seedShowDiffStore builds a binding under the default state root with one
// completed round: a diff patch on disk and the log entries that name it. It
// is store-only -- no harness and no network -- so the run-based assertions
// below reach no builder.
func seedShowDiffStore(t *testing.T, name string) (*store.Store, relevo.Runtime) {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	patch := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"
	if err := os.WriteFile(s.DiffPath(name, 1), []byte(patch), 0o644); err != nil {
		t.Fatalf("write diff: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindDiff, Note: "1 file, +1 -0", Confirmed: true},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return s, relevo.Runtime{Store: s, Now: time.Now}
}

// TestShowAbsorbsDiffAndLog pins §4.2: `show --diff --stat` and `show --log`
// route to the same diff and log helpers the removed verbs used, so the
// subcommand's stdout is byte-identical to the helper's.
func TestShowAbsorbsDiffAndLog(t *testing.T) {
	const name = "showabsorb"
	_, rt := seedShowDiffStore(t, name)

	want, _, err := captureOutput(t, func() error { return printDiff(rt, name, 0, true, false, false) })
	if err != nil {
		t.Fatalf("printDiff: %v", err)
	}
	got, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--diff", "--stat"}) })
	if err != nil {
		t.Fatalf("run show --diff --stat: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("show --diff --stat = %q, want the printDiff helper's %q", got, want)
	}

	wantLog, _, err := captureOutput(t, func() error { return printLog(rt, name, 0, 0, false, false, true) })
	if err != nil {
		t.Fatalf("printLog: %v", err)
	}
	gotLog, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--log"}) })
	if err != nil {
		t.Fatalf("run show --log: %v", err)
	}
	if string(gotLog) != string(wantLog) {
		t.Errorf("show --log = %q, want the printLog helper's %q", gotLog, wantLog)
	}
}

// TestShowAbsorbedFlagCombinations pins §4.2's refusals: --stat/--anchors need
// --diff/--drift, and --follow/--after need --log. Each exits 2 before any
// runtime is built.
func TestShowAbsorbedFlagCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"show", "api", "--stat"},
		{"show", "api", "--anchors"},
		{"show", "api", "--plan", "--stat"},
		{"show", "api", "--follow"},
		{"show", "api", "--after", "2"},
	} {
		_, _, err := captureOutput(t, func() error { return run(args) })
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%v: run = %v, want exit code 2", args, err)
		}
	}
}

// TestShowSectionFlagsGateAndFindings pins §4.2's two new sections in the pure
// resolver: --gate is a section, a non-empty --findings id is another, and
// either with --report is a usage error.
func TestShowSectionFlagsGateAndFindings(t *testing.T) {
	section, err := showSectionFlags(showSectionArgs{gate: true})
	if err != nil || section != relevo.ShowGate {
		t.Errorf("--gate: section = %q err = %v, want %q", section, err, relevo.ShowGate)
	}

	section, err = showSectionFlags(showSectionArgs{findingsID: "7f2a3c1d"})
	if err != nil || section != relevo.ShowFindings {
		t.Errorf("--findings: section = %q err = %v, want %q", section, err, relevo.ShowFindings)
	}

	if _, err := showSectionFlags(showSectionArgs{report: true, findingsID: "7f2a3c1d"}); err == nil {
		t.Error("--report --findings: err = nil, want a usage error (more than one section)")
	}
}

// TestShowSectionFlagsSummaryAndArtifacts pins §2's two new sections in the
// pure resolver: --summary and --artifacts each name one, a non-empty
// --artifact rel names the artifacts section too, and either with --report is
// a usage error.
func TestShowSectionFlagsSummaryAndArtifacts(t *testing.T) {
	section, err := showSectionFlags(showSectionArgs{summary: true})
	if err != nil || section != relevo.ShowSummary {
		t.Errorf("--summary: section = %q err = %v, want %q", section, err, relevo.ShowSummary)
	}

	section, err = showSectionFlags(showSectionArgs{artifacts: true})
	if err != nil || section != relevo.ShowArtifacts {
		t.Errorf("--artifacts: section = %q err = %v, want %q", section, err, relevo.ShowArtifacts)
	}

	section, err = showSectionFlags(showSectionArgs{artifactRel: "summary.md"})
	if err != nil || section != relevo.ShowArtifacts {
		t.Errorf("--artifact: section = %q err = %v, want %q", section, err, relevo.ShowArtifacts)
	}

	if _, err := showSectionFlags(showSectionArgs{report: true, summary: true}); err == nil {
		t.Error("--report --summary: err = nil, want a usage error (more than one section)")
	}
	if _, err := showSectionFlags(showSectionArgs{report: true, artifacts: true}); err == nil {
		t.Error("--report --artifacts: err = nil, want a usage error (more than one section)")
	}
}

// TestShowGateAndFindings pins §4.2: `show --gate` prints the round's gate log
// and `show --findings ID` prints a consult's findings, both read through
// rt.Store.ReadFile. It is store-only -- no harness and no network.
func TestShowGateAndFindings(t *testing.T) {
	const name = "showgate"
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	gateBody := "make check -- PASS (exit 0, 1m40s)\n"
	if err := os.WriteFile(s.GateLogPath(name, 1), []byte(gateBody), 0o644); err != nil {
		t.Fatalf("write gate log: %v", err)
	}
	const id = "7f2a3c1d"
	findingsBody := "verdict: accepted\n"
	if err := os.WriteFile(s.FindingsPath(name, 1, id), []byte(findingsBody), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	got, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--round", "1", "--gate"}) })
	if err != nil {
		t.Fatalf("show --gate: %v", err)
	}
	if string(got) != gateBody {
		t.Errorf("show --gate = %q, want %q", got, gateBody)
	}

	got, _, err = captureOutput(t, func() error {
		return run([]string{"show", name, "--round", "1", "--findings", id})
	})
	if err != nil {
		t.Fatalf("show --findings: %v", err)
	}
	if string(got) != findingsBody {
		t.Errorf("show --findings = %q, want %q", got, findingsBody)
	}
}

// TestShowSummaryArtifactsCLI pins §2's new output: --summary prints the
// round's summary.md, --artifacts one line per file (summary.md first),
// --artifact <rel> the file's raw bytes with nothing added, --json the
// rel/size/mtime fields, and a writer round answers Missing. It is store-only
// -- no harness and no network.
func TestShowSummaryArtifactsCLI(t *testing.T) {
	const name = "showsummary"
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{
		Name: name, CWD: t.TempDir(), Round: 2, State: store.StateActive,
		Shape: store.ShapeReader, Role: "reviewer",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	dir := s.ArtifactDir(name, 1, "reviewer")
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const summary = "# the summary\n"
	const site = "<html>\n"
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(summary), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "index.html"), []byte(site), 0o644); err != nil {
		t.Fatalf("write site: %v", err)
	}

	got, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--round", "1", "--summary"}) })
	if err != nil || string(got) != summary {
		t.Errorf("show --summary = %q (err %v), want %q", got, err, summary)
	}

	got, _, err = captureOutput(t, func() error { return run([]string{"show", name, "--round", "1", "--artifacts"}) })
	if err != nil {
		t.Fatalf("show --artifacts: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "summary.md") || !strings.HasSuffix(lines[1], "site/index.html") {
		t.Errorf("show --artifacts = %q, want one line each, summary.md first", got)
	}

	got, _, err = captureOutput(t, func() error {
		return run([]string{"show", name, "--round", "1", "--artifact", "site/index.html"})
	})
	if err != nil || string(got) != site {
		t.Errorf("show --artifact = %q (err %v), want the raw bytes %q", got, err, site)
	}

	got, _, err = captureOutput(t, func() error {
		return run([]string{"show", name, "--round", "1", "--json", "--artifacts"})
	})
	if err != nil {
		t.Fatalf("show --json --artifacts: %v", err)
	}
	for _, want := range []string{`"rel": "summary.md"`, `"size":`, `"mtime":`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("show --json --artifacts is missing %s:\n%s", want, got)
		}
	}

	// A writer round with no artifact dir answers Missing, not an error.
	const writer = "showsummary-writer"
	if err := s.Save(store.Binding{Name: writer, CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save(writer): %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := s.AppendLog(writer, e); err != nil {
			t.Fatalf("AppendLog(writer): %v", err)
		}
	}
	got, _, err = captureOutput(t, func() error { return run([]string{"show", writer, "--round", "1", "--summary"}) })
	if err != nil || string(got) != "no summary for round 1\n" {
		t.Errorf("show --summary on a writer = %q (err %v), want %q", got, err, "no summary for round 1\n")
	}
}
