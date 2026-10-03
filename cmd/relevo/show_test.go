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
// none given defaults to prompt, exactly one wins, more than one is a usage
// error. showSectionFlags is a pure function, so this never executes the
// subcommand -- CI launches no harness.
func TestShowSectionFlagsConflict(t *testing.T) {
	section, err := showSectionFlags(showSectionArgs{})
	if err != nil {
		t.Fatalf("no flags: err = %v, want nil", err)
	}
	if section != relevo.ShowPrompt {
		t.Errorf("no flags: section = %q, want %q (default)", section, relevo.ShowPrompt)
	}

	section, err = showSectionFlags(showSectionArgs{report: true})
	if err != nil {
		t.Fatalf("--report: err = %v, want nil", err)
	}
	if section != relevo.ShowReport {
		t.Errorf("--report: section = %q, want %q", section, relevo.ShowReport)
	}

	if _, err := showSectionFlags(showSectionArgs{prompt: true, report: true}); err == nil {
		t.Error("--prompt --report: err = nil, want a usage error (more than one section)")
	}
}

// TestShowRetiredFlagsAreUnknown pins that a retired section spelling is the
// flag package's own error, before any runtime is built: --plan is no longer
// registered. It fails at the parse, so no harness is reached.
func TestShowRetiredFlagsAreUnknown(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"show", "api", "--round", "1", "--plan"})
	})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("show --plan: err = %v, want \"flag provided but not defined\"", err)
	}
}

// seedShowDiffStore builds a binding under the default state root with one
// completed round: a diff patch as a round_file row and the log entries that
// name it. It is store-only -- no harness and no network -- so the run-based
// assertions below reach no builder.
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
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile(name, 1, s.DiffPath(name, 1), []byte(patch))
	}); err != nil {
		t.Fatalf("PutRoundFile diff: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
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
		{"show", "api", "--prompt", "--stat"},
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
// pure resolver: --output and --artifacts each name one, a non-empty
// --artifact rel names the artifacts section too, and either with --report is
// a usage error.
func TestShowSectionFlagsSummaryAndArtifacts(t *testing.T) {
	section, err := showSectionFlags(showSectionArgs{output: true})
	if err != nil || section != relevo.ShowOutput {
		t.Errorf("--output: section = %q err = %v, want %q", section, err, relevo.ShowOutput)
	}

	section, err = showSectionFlags(showSectionArgs{artifacts: true})
	if err != nil || section != relevo.ShowArtifacts {
		t.Errorf("--artifacts: section = %q err = %v, want %q", section, err, relevo.ShowArtifacts)
	}

	section, err = showSectionFlags(showSectionArgs{artifactRel: "summary.md"})
	if err != nil || section != relevo.ShowArtifacts {
		t.Errorf("--artifact: section = %q err = %v, want %q", section, err, relevo.ShowArtifacts)
	}

	if _, err := showSectionFlags(showSectionArgs{report: true, output: true}); err == nil {
		t.Error("--report --output: err = nil, want a usage error (more than one section)")
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
	if err := s.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	gateBody := "make check -- PASS (exit 0, 1m40s)\n"
	if err := os.WriteFile(s.GateLogPath(name, 1), []byte(gateBody), 0o644); err != nil {
		t.Fatalf("write gate log: %v", err)
	}
	const id = "7f2a3c1d"
	findingsBody := "verdict: accepted\n"
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile(name, 1, s.FindingsPath(name, 1, id), []byte(findingsBody))
	}); err != nil {
		t.Fatalf("PutRoundFile findings: %v", err)
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

// TestShowOutputArtifactsCLI pins §2's new output: --output prints the
// round's output file, --artifacts one line per file (the output first),
// --artifact <rel> the file's raw bytes with nothing added, --json the
// rel/size/mtime fields, and a writer round answers Missing. It is store-only
// -- no harness and no network.
func TestShowOutputArtifactsCLI(t *testing.T) {
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
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
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

	got, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--round", "1", "--output"}) })
	if err != nil || string(got) != summary {
		t.Errorf("show --output = %q (err %v), want %q", got, err, summary)
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
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := s.AppendLog(writer, e); err != nil {
			t.Fatalf("AppendLog(writer): %v", err)
		}
	}
	got, _, err = captureOutput(t, func() error { return run([]string{"show", writer, "--round", "1", "--output"}) })
	if err != nil || string(got) != "no output for round 1\n" {
		t.Errorf("show --output on a writer = %q (err %v), want %q", got, err, "no output for round 1\n")
	}
}

// seedShowPendingStore seeds a live binding under the default state root with
// one completed round, the report file --report prints, and one pending
// mastermind-bound report entry. Store-only: no harness, no network. It
// returns the store so a test can inspect the entry's route after the run.
func seedShowPendingStore(t *testing.T, name string) *store.Store {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.MkdirAll(s.OutDir(name), 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.WriteFile(s.ReportPath(name, 1), []byte("# round 1 report\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Payload: "round 1 report", Path: s.ReportPath(name, 1),
		},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return s
}

// claimedRoute returns the route of name's confirmed mastermind-bound entry and
// whether one exists.
func claimedRoute(t *testing.T, s *store.Store, name string) (string, bool) {
	t.Helper()
	entries, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Confirmed {
			return e.Route, true
		}
	}
	return "", false
}

// TestShowPeekFlagLeavesPendingMasterMindPayload pins the verb's claim rule:
// `show --report` prints the pending report and claims that very payload with
// route "show", `show --report --peek` prints the same section but claims
// nothing, and a section that is not the report claims nothing either. Both
// are store-only: no harness, no network.
func TestShowPeekFlagLeavesPendingMasterMindPayload(t *testing.T) {
	t.Run("plain report claims with show's route", func(t *testing.T) {
		const name = "showpeekclaim"
		s := seedShowPendingStore(t, name)

		if _, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--report"}) }); err != nil {
			t.Fatalf("show --report: %v", err)
		}
		route, confirmed := claimedRoute(t, s, name)
		if !confirmed || route != "show" {
			t.Errorf("confirmed/route = %v/%q, want true/\"show\"", confirmed, route)
		}
	})

	t.Run("peek leaves it pending", func(t *testing.T) {
		const name = "showpeekleave"
		s := seedShowPendingStore(t, name)

		if _, _, err := captureOutput(t, func() error {
			return run([]string{"show", name, "--report", "--peek"})
		}); err != nil {
			t.Fatalf("show --report --peek: %v", err)
		}
		if route, confirmed := claimedRoute(t, s, name); confirmed {
			t.Errorf("show --report --peek confirmed the pending payload (route %q); want it left pending", route)
		}
	})

	t.Run("another section leaves the report pending", func(t *testing.T) {
		const name = "showpeekother"
		s := seedShowPendingStore(t, name)

		if _, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--prompt"}) }); err != nil {
			t.Fatalf("show --prompt: %v", err)
		}
		if route, confirmed := claimedRoute(t, s, name); confirmed {
			t.Errorf("show --prompt confirmed the pending report payload (route %q); want it left pending", route)
		}
	})
}

// TestPrintShowSanitizesControlBytes pins that show's display path sanitises the
// round's output while --artifact stays a raw file copy.
func TestPrintShowSanitizesControlBytes(t *testing.T) {
	const name = "showsanitize"
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
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	dir := s.ArtifactDir(name, 1, "reviewer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const summary = "# the summary \x1b[2J\n"
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(summary), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	got, _, err := captureOutput(t, func() error { return run([]string{"show", name, "--round", "1", "--output"}) })
	if err != nil {
		t.Fatalf("show --output: %v", err)
	}
	if strings.ContainsRune(string(got), '\x1b') {
		t.Errorf("show --output = %q, want the control byte replaced", got)
	}
	if !strings.Contains(string(got), "\uFFFD") {
		t.Errorf("show --output = %q, want a replacement rune", got)
	}

	// The artifact path is a file-copy contract: its bytes stay raw.
	raw, err := os.ReadFile(filepath.Join(dir, "summary.md"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(raw), "\x1b") {
		t.Errorf("summary.md = %q, want the stored bytes untouched", raw)
	}
}
