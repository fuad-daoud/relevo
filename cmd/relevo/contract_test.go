package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// update rewrites every testdata/contract/*.golden file this test binary
// touches, from the unmodified code's own output. No package under this
// round declares an "update" flag yet (verified with grep before adding it).
var update = flag.Bool("update", false, "rewrite testdata/contract/*.golden")

// assertGolden compares got against testdata/contract/<name>.golden, or
// writes it there when -update is set.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	filename := name
	if !strings.HasSuffix(filename, ".golden") {
		filename += ".golden"
	}
	path := filepath.Join("testdata", "contract", filename)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s: re-run with 'go test ./cmd/relevo -run Contract -update' to generate", path)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch in %s at byte %d: re-run with 'go test ./cmd/relevo -run Contract -update' to update\n--- got ---\n%s\n--- want ---\n%s",
			path, firstDiffByte(got, want), got, want)
	}
}

// firstDiffByte returns the index of the first byte at which a and b differ,
// or the length of the shorter one when one is a prefix of the other.
func firstDiffByte(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

var (
	// rfc3339Pattern covers a timestamp with or without fractional seconds,
	// Z or a numeric offset -- every shape time.Time's JSON marshalling and
	// time.Format(time.RFC3339) produce.
	rfc3339Pattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	// ulidPattern matches a bare 26-char Crockford ULID: relevo's own ids
	// (round ids, binding record ids, ...) that carry no "pl_" prefix.
	ulidPattern = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`)
	// mastermindIDPattern matches relevo's "pl_" mastermind ids, minted separately
	// from the bare ULIDs above.
	mastermindIDPattern = regexp.MustCompile(`\bpl_[0-9A-Za-z]+\b`)
)

// normalize replaces volatile substrings of b with fixed placeholders, so a
// golden captures the shape of an output rather than one run's host, clock
// or ids. Each of roots (a fixture's temp directories) becomes <ROOT0>,
// <ROOT1>, ... in the order given, substituted longest-first so a root
// nested inside another is not half-replaced by its parent first.
func normalize(b []byte, roots ...string) []byte {
	s := string(b)

	type labeledRoot struct{ root, label string }
	var labeled []labeledRoot
	for i, r := range roots {
		if r != "" {
			labeled = append(labeled, labeledRoot{r, fmt.Sprintf("<ROOT%d>", i)})
		}
	}
	sort.Slice(labeled, func(i, j int) bool { return len(labeled[i].root) > len(labeled[j].root) })
	for _, lr := range labeled {
		s = strings.ReplaceAll(s, lr.root, lr.label)
	}

	if v := buildVersion(); v != "" {
		s = strings.ReplaceAll(s, v, "<VERSION>")
	}
	s = mastermindIDPattern.ReplaceAllString(s, "<PLANNER>")
	s = ulidPattern.ReplaceAllString(s, "<ID>")
	s = rfc3339Pattern.ReplaceAllString(s, "<TIME>")

	return []byte(s)
}

// ---------------------------------------------------------------------
// C1 + C2: relevo status --json / --all --json / --name X --json, and
// status --line / --line --json.
// ---------------------------------------------------------------------

// statusFixture is what seedStatusFixture built, so each subtest can
// normalize with the same roots and (for the statusline) mastermind id.
type statusFixture struct {
	roots        []string
	mastermindID string
}

// seedStatusFixture seeds one store with four bindings: "webshop" (ACTIVE,
// round 2, with round 1's report still pending delivery, owned by a
// registered mastermind so the statusline has a row), "atlas" (a planner
// reader on the same mastermind whose confirmed round 1 output is delivered,
// so its row carries the reader's word and the one shape key a writer omits),
// "archived" (DONE, no mastermind) and
// "hosted" (a remote-server binding, no server actually contacted -- rt.Remote
// stays nil, so relevo never dials out). Every CWD lives under the store's own
// root, so normalizing that one root cleans every path in the output. No
// RoundStartedAt or Progress is set anywhere, so the live-diff and
// quiet-for computations (which need a git worktree and a real clock) stay
// nil instead of reaching outside the fixture.
func seedStatusFixture(t *testing.T) statusFixture {
	t.Helper()
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)

	reg := mastermindRegistryAt(t, stateHome)
	rec, err := reg.Create(mastermind.Record{
		ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess-fixture",
		CWD: filepath.Join(root, "mastermind"),
	})
	if err != nil {
		t.Fatalf("mastermind Create: %v", err)
	}

	fixedTS := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)

	if err := s.Save(store.Binding{
		Name: "webshop", CWD: filepath.Join(root, "work", "webshop"),
		Round: 2, State: store.StateActive, MasterMindID: rec.ID,
	}); err != nil {
		t.Fatalf("Save webshop: %v", err)
	}
	// Confirmed: false is what makes this the pending report PendingForMasterMind
	// finds: the oldest undelivered mastermind payload.
	if err := s.AppendLog("webshop", store.LogEntry{
		TS: fixedTS, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: "/x/001-report.md",
	}); err != nil {
		t.Fatalf("AppendLog webshop: %v", err)
	}

	// Confirmed: true is what makes atlas's report delivered rather than
	// pending, so its row reads ARTIFACT IN and carries the reader's shape.
	if err := s.Save(store.Binding{
		Name: "atlas", CWD: filepath.Join(root, "work", "atlas"),
		Round: 2, State: store.StateActive, MasterMindID: rec.ID,
		Role: "planner", Shape: store.ShapeReader,
	}); err != nil {
		t.Fatalf("Save atlas: %v", err)
	}
	if err := s.AppendLog("atlas", store.LogEntry{
		TS: fixedTS, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: "/x/001-plan.md", Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog atlas: %v", err)
	}

	if err := s.Save(store.Binding{
		Name: "archived", CWD: filepath.Join(root, "work", "archived"),
		Round: 1, State: store.StateDone,
	}); err != nil {
		t.Fatalf("Save archived: %v", err)
	}

	if err := s.Save(store.Binding{
		Name: "hosted", CWD: filepath.Join(root, "work", "hosted"),
		Round: 1, State: store.StateActive,
		Builder: store.Endpoint{Mode: store.ModeRemote, Server: "relevo.example.test"},
	}); err != nil {
		t.Fatalf("Save hosted: %v", err)
	}

	return statusFixture{roots: []string{root}, mastermindID: rec.ID}
}

func TestContractStatus(t *testing.T) {
	fx := seedStatusFixture(t)

	for _, c := range []struct {
		golden string
		args   []string
	}{
		{"status", []string{"status", "--json"}},
		{"status-all", []string{"status", "--all", "--json"}},
		{"status-name", []string{"status", "--name", "webshop", "--json"}},
	} {
		stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", c.args, err, stderr)
		}
		assertGolden(t, c.golden, normalize(stdout, fx.roots...))
	}
}

// TestStatusDocumentAlwaysNamesTheActor pins that a builder binding's status
// JSON names its actor, "builder", and carries no pre-A4 role or builder_* key.
func TestStatusDocumentAlwaysNamesTheActor(t *testing.T) {
	fx := seedStatusFixture(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--name", "webshop", "--json"})
	})
	if err != nil {
		t.Fatalf("status --json: %v (stderr: %s)", err, stderr)
	}
	doc := string(normalize(stdout, fx.roots...))
	if !strings.Contains(doc, `"actor": "builder"`) {
		t.Errorf("status document = %s, want actor \"builder\"", doc)
	}
	for _, key := range []string{`"role"`, `"builder_candidate"`, `"builder_name"`, `"builder_kind"`, `"builder_status"`, `"builder_definition"`, `"builder_definition_custom"`} {
		if strings.Contains(doc, key) {
			t.Errorf("status document still carries %s:\n%s", key, doc)
		}
	}
}

func TestContractStatusLine(t *testing.T) {
	fx := seedStatusFixture(t)
	t.Setenv("RELEVO_MASTERMIND", fx.mastermindID)

	for _, c := range []struct {
		golden string
		args   []string
	}{
		{"statusline", []string{"status", "--line"}},
		{"statusline-json", []string{"status", "--line", "--json"}},
	} {
		stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", c.args, err, stderr)
		}
		assertGolden(t, c.golden, normalize(stdout, fx.roots...))
	}
}

// ---------------------------------------------------------------------
// C3: relevo show <name> --json, for every section flag, plus
// show <name> --log --after 0 --json.
// ---------------------------------------------------------------------

const showFixtureFindingsID = "7f2a3c1d"

// seedShowSectionsFixture seeds one binding with every section's artifact on
// disk: a plan, a report, a diff, a drift patch, a gate log and a findings
// file, plus the log entries that name the diff and drift rounds. No
// transcript file is written, so the transcript golden pins today's Missing
// result for a round with no builder log and no stream -- the round's plain
// behaviour, not a stand-in.
func seedShowSectionsFixture(t *testing.T) (name string, roots []string) {
	t.Helper()
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	name = "showsections"

	if err := s.Save(store.Binding{Name: name, CWD: filepath.Join(root, "work"), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := os.WriteFile(s.PromptPath(name, 1), []byte("# plan body\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if err := os.WriteFile(s.ReportPath(name, 1), []byte("# report body\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	diffPatch := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"
	if err := os.WriteFile(s.DiffPath(name, 1), []byte(diffPatch), 0o644); err != nil {
		t.Fatalf("write diff: %v", err)
	}
	driftPatch := "diff --git a/drift.txt b/drift.txt\n--- a/drift.txt\n+++ b/drift.txt\n@@ -1 +1,2 @@\n drift\n+round1\n"
	if err := os.WriteFile(s.DriftPath(name, 1), []byte(driftPatch), 0o644); err != nil {
		t.Fatalf("write drift: %v", err)
	}
	if err := os.WriteFile(s.GateLogPath(name, 1), []byte("make check -- PASS (exit 0, 1m40s)\n"), 0o644); err != nil {
		t.Fatalf("write gate log: %v", err)
	}
	if err := os.WriteFile(s.FindingsPath(name, 1, showFixtureFindingsID), []byte("verdict: accepted\n"), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	fixedTS := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	for i, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Note: "round one"},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindDiff, Confirmed: true, Note: "1 file, +1 -0"},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindDrift, Confirmed: true, Note: "1 file, +1 -1"},
	} {
		e.TS = fixedTS.Add(time.Duration(i) * time.Minute)
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog %d: %v", i, err)
		}
	}

	return name, []string{root}
}

func TestContractShow(t *testing.T) {
	name, roots := seedShowSectionsFixture(t)

	for _, c := range []struct {
		golden string
		args   []string
	}{
		{"show-prompt", []string{"show", name, "--round", "1", "--prompt", "--json"}},
		{"show-report", []string{"show", name, "--round", "1", "--report", "--json"}},
		{"show-diff", []string{"show", name, "--round", "1", "--diff", "--json"}},
		{"show-drift", []string{"show", name, "--round", "1", "--drift", "--json"}},
		{"show-log", []string{"show", name, "--round", "1", "--log", "--json"}},
		{"show-transcript", []string{"show", name, "--round", "1", "--transcript", "--json"}},
		{"show-gate", []string{"show", name, "--round", "1", "--gate", "--json"}},
		{"show-findings", []string{"show", name, "--round", "1", "--findings", showFixtureFindingsID, "--json"}},
	} {
		stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", c.args, err, stderr)
		}
		assertGolden(t, c.golden, normalize(stdout, roots...))
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"show", name, "--log", "--after", "0", "--json"})
	})
	if err != nil {
		t.Fatalf("show --log --after 0 --json: %v (stderr: %s)", err, stderr)
	}
	assertGolden(t, "show-log-after", normalize(stdout, roots...))
}

// ---------------------------------------------------------------------
// C4: relevo history --json, --binding, --mastermind, --limit 1, -q 'outcome:done'.
// ---------------------------------------------------------------------

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// seedHistoryFixture opens relevo.db directly (the way
// cmd/relevo/history_test.go's TestHistoryPlainListsRounds does) and writes
// two bindings' rounds with fixed timestamps, so ordering ("newest first",
// "--limit 1") is deterministic before normalize erases the actual clock
// values. CWDs are plain literal strings, not temp directories: db.Binding.CWD
// is never read from disk by history, so it needs no root normalization.
func seedHistoryFixture(t *testing.T) {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	dbPath := filepath.Join(stateHome, "relevo", "relevo.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()

	t0 := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)

	// UpsertBinding's mastermind_id column references mastermind(id), so the
	// mastermind row must exist first.
	if _, err := d.UpsertMasterMind(db.MasterMind{
		ID: "pl_fixturehistory", HarnessKind: "claude", SessionID: "sess-fixturehistory",
		FirstSeen: t0, LastSeen: t0,
	}); err != nil {
		t.Fatalf("UpsertMasterMind: %v", err)
	}

	alphaID, err := d.UpsertBinding(db.Binding{
		Name: "hist-alpha", CWD: "/repo/hist-alpha", BuilderMode: "headless",
		MasterMindID: strPtr("pl_fixturehistory"), CreatedAt: t0, IngestSource: db.IngestLive,
		Ticket: strPtr("o/r#607"),
	})
	if err != nil {
		t.Fatalf("UpsertBinding hist-alpha: %v", err)
	}
	closedAlpha := t0.Add(30 * time.Minute)
	if _, err := d.UpsertRound(db.Round{
		BindingID: alphaID, Number: 1, StartedAt: t0, ClosedAt: &closedAlpha,
		Outcome: db.OutcomeReported, ReportOutcome: strPtr("done"), Commits: intPtr(2),
	}); err != nil {
		t.Fatalf("UpsertRound hist-alpha/1: %v", err)
	}

	betaID, err := d.UpsertBinding(db.Binding{
		Name: "hist-beta", CWD: "/repo/hist-beta", BuilderMode: "headless",
		CreatedAt: t0.Add(5 * time.Minute), IngestSource: db.IngestLive,
		Ticket: strPtr("#607"),
	})
	if err != nil {
		t.Fatalf("UpsertBinding hist-beta: %v", err)
	}
	if _, err := d.UpsertRound(db.Round{
		BindingID: betaID, Number: 1, StartedAt: t0.Add(5 * time.Minute), Outcome: db.OutcomeOpen,
	}); err != nil {
		t.Fatalf("UpsertRound hist-beta/1: %v", err)
	}
	closedBeta2 := t0.Add(20 * time.Minute)
	if _, err := d.UpsertRound(db.Round{
		BindingID: betaID, Number: 2, StartedAt: t0.Add(10 * time.Minute), ClosedAt: &closedBeta2,
		Outcome: db.OutcomeReported, ReportOutcome: strPtr("halted"), Commits: intPtr(1),
	}); err != nil {
		t.Fatalf("UpsertRound hist-beta/2: %v", err)
	}
}

func TestContractHistory(t *testing.T) {
	seedHistoryFixture(t)

	for _, c := range []struct {
		golden string
		args   []string
	}{
		{"history-binding", []string{"history", "--json", "--binding", "hist-alpha"}},
		// --mastermind matches db's mastermind.session_id, not the mastermind's own
		// id (internal/db/read.go: "mastermind_id IN (SELECT id FROM mastermind
		// WHERE session_id = ?)"), hence the session id here rather than
		// "pl_fixturehistory".
		{"history-mastermind", []string{"history", "--json", "--mastermind", "sess-fixturehistory"}},
		{"history-limit", []string{"history", "--json", "--limit", "1"}},
		// "reported" is a valid db.Round.Outcome that both fixture bindings
		// carry a round for (hist-alpha/1, hist-beta/2), so the query
		// matches a non-empty set of rows.
		{"history-q", []string{"history", "--json", "-q", "outcome:reported"}},
		// A bare --ticket matches every stored ticket ending in #N, and
		// --by ticket regroups the matches by their stored form.
		{"history-ticket", []string{"history", "--json", "--ticket", "#607", "--by", "ticket"}},
	} {
		stdout, _, _ := captureOutput(t, func() error { return run(c.args) })
		assertGolden(t, c.golden, normalize(stdout))
	}

	// "outcome:done" is not a valid db.Round.Outcome (only reported, halted,
	// exited, switched, done_no_report, open are); this pins today's actual
	// rejected-query behaviour -- the usage code, exit 2, and the JSON error
	// envelope on stderr, nothing on stdout -- separately from the valid-query
	// rows above, since agents do hit it.
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"history", "--json", "-q", "outcome:done"})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
	if len(stdout) != 0 {
		t.Errorf("history -q outcome:done: stdout = %q, want empty", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("history -q outcome:done: stderr = %q, want empty before report", stderr)
	}
	var buf bytes.Buffer
	if code := report(&buf, err, true); code != 2 {
		t.Errorf("history -q outcome:done: report exit = %d, want 2", code)
	}
	assertGolden(t, "history-q-invalid", normalize(buf.Bytes()))
}

// ---------------------------------------------------------------------
// C6: every other surviving --json output. serve status --json is already
// pinned (C5/C9) and skipped, as the plan's rules for this round say to.
// grep -n 'fs.Bool("json"' cmd/relevo/*.go leaves exactly these two beyond
// status/show/history above.
// ---------------------------------------------------------------------

func TestContractMasterMindList(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))

	reg := mastermindRegistryAt(t, stateHome)
	if _, err := reg.Create(mastermind.Record{
		ID: "pl_ccccccccdddd", Name: "architect-1", HarnessKind: "claude", SessionID: "sess-fixture",
		CWD: "/repo/architect-1",
	}); err != nil {
		t.Fatalf("mastermind Create: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"mastermind", "list", "--json"}) })
	if err != nil {
		t.Fatalf("mastermind list --json: %v (stderr: %s)", err, stderr)
	}
	assertGolden(t, "mastermind-list", normalize(stdout))
}

func TestContractConfigLog(t *testing.T) {
	initRoot(t)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`})
	}); err != nil {
		t.Fatalf("config set candidates: %v (stderr: %s)", err, stderr)
	}

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"config", "log", "--json"}) })
	if err != nil {
		t.Fatalf("config log --json: %v (stderr: %s)", err, stderr)
	}
	assertGolden(t, "config-log", normalize(stdout))

	stdout, stderr, err = captureOutput(t, func() error { return run([]string{"config", "log", "--rev", "1", "--json"}) })
	if err != nil {
		t.Fatalf("config log --rev 1 --json: %v (stderr: %s)", err, stderr)
	}
	assertGolden(t, "config-log-rev", normalize(stdout))
}

// ---------------------------------------------------------------------
// C8a: relevo wait exit codes 0, 2, 3, 4, 5, 124. Each case's Line is
// captured with --peek, so no fixture needs a real report file on disk:
// internal/relevo/wait_test.go's TestWaitOutcome and TestWaitTimesOut
// already pin WaitOutcome's own classification at the pure-function level;
// this pins the CLI's exit-code mapping and stdout for the same six codes.
// ---------------------------------------------------------------------

func seedWaitBinding(t *testing.T, name string, b store.Binding, entries []store.LogEntry) {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	b.Name = name
	if b.CWD == "" {
		b.CWD = filepath.Join(root, "work", name)
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
	for _, e := range entries {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog %s: %v", name, err)
		}
	}
}

func TestContractWaitExitCodes(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	seedWaitBinding(t, "wait-closed", store.Binding{Round: 1, State: store.StateActive}, []store.LogEntry{
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md"},
	})
	seedWaitBinding(t, "wait-unmarked", store.Binding{Round: 1, State: store.StateActive}, []store.LogEntry{
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "unmarked"},
	})
	seedWaitBinding(t, "wait-needsyou", store.Binding{
		Round: 1, State: store.StateNeedsYou,
		Halt: "round 1 has run past 2h0m0s", HaltAt: time.Unix(1757000000, 0).UTC(),
	}, nil)
	seedWaitBinding(t, "wait-gone", store.Binding{Round: 1, State: store.StateDone}, nil)
	seedWaitBinding(t, "wait-halted", store.Binding{Round: 1, State: store.StateActive}, []store.LogEntry{
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Outcome: "halted"},
	})
	seedWaitBinding(t, "wait-timeout", store.Binding{Round: 1, State: store.StateActive}, []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	})

	for _, c := range []struct {
		golden   string
		name     string
		wantCode int
	}{
		{"wait-0", "wait-closed", 0},
		{"wait-2", "wait-unmarked", 2},
		{"wait-3", "wait-needsyou", 3},
		{"wait-4", "wait-gone", 4},
		{"wait-5", "wait-halted", 5},
		{"wait-124", "wait-timeout", 124},
	} {
		// --timeout 1ns: the first pass always runs before any sleep, and by
		// the time it finishes 1ns has already elapsed, so a round with
		// nothing to report times out at once instead of the real one-second
		// interval cmdWait hands to Wait.
		stdout, _, runErr := captureOutput(t, func() error {
			return run([]string{"wait", "--name", c.name, "--round", "1", "--timeout", "1ns", "--peek"})
		})
		gotCode := 0
		if runErr != nil {
			var ec exitCodeErr
			if !errors.As(runErr, &ec) {
				t.Fatalf("%s: run error %v is not an exitCodeErr", c.name, runErr)
			}
			gotCode = ec.code
		}
		if gotCode != c.wantCode {
			t.Errorf("%s: exit code = %d, want %d (stdout %q)", c.name, gotCode, c.wantCode, stdout)
		}
		assertGolden(t, c.golden, normalize(stdout))
	}
}

// ---------------------------------------------------------------------
// C11: relevo config export.
// ---------------------------------------------------------------------

// TestContractConfigExport seeds candidates, policy and actors -- the three
// sections cmd/relevo/config_test.go's own fixtures already exercise
// together (TestConfigBareShowsThreeHeadings, TestConfigExportImportRoundTrip).
// The other five config.Sections (agents, roles, prices, servers, hooks) are
// left at their zero value: this round did not track down a valid schema for
// each of them, and a wrong one would fail config's own validation rather
// than pin anything. See the round's report.
func TestContractConfigExport(t *testing.T) {
	initRoot(t)

	for _, args := range [][]string{
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`},
		{"config", "set", "policy.order.builder", `["claude/p/m"]`},
		{"config", "set", "actors", `{"builder":{"agent":"plan-executor","candidates":["claude/p/m"]}}`},
	} {
		if _, stderr, err := captureOutput(t, func() error { return run(args) }); err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
	}

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"config", "export"}) })
	if err != nil {
		t.Fatalf("config export: %v (stderr: %s)", err, stderr)
	}
	assertGolden(t, "config-export", normalize(stdout))
}

// ---------------------------------------------------------------------
// C12: every `relevo <verb> ...` command line in internal/harness/agents/*.md,
// claude-plugin/commands/*.md, internal/mcp/instructions.go and
// internal/delivery/push.go.
// ---------------------------------------------------------------------

// relevoCommandLinePattern is the plan's own extraction regexp.
var relevoCommandLinePattern = regexp.MustCompile(`relevo ([a-z-]+)((?: --?[a-z-]+)*)`)

// relevoFlaggedCommandLinePattern is the same pattern narrowed to a
// mandatory flag. instructions.go and push.go carry their command examples
// as plain sentences inside one long Go string, with no markdown backtick to
// set them off from the surrounding prose -- and that prose says things like
// "relevo is handing you round events" and "relevo knows about", which the
// bare pattern also matches, "is" and "knows" read as verbs. Every actual
// command example in those two files carries at least one flag
// (`relevo wait --name ...`, `relevo show %s --round %d --%s`), so requiring
// one here is what tells a reference from a sentence, at the cost of no
// longer checking a handful of bare mentions ("relevo show command", "relevo
// status to catch up") that are already covered by a flagged mention of the
// same verb in the same file.
//
// Its flag token requires a letter first ([a-z][a-z-]*, not [a-z-]+): the
// plan's own pattern's class also allows a bare run of hyphens, which turns
// prose like "relevo verb -- bind" ('verb' the noun, an em dash) into a
// spurious one-flag match on "-" itself.
var relevoFlaggedCommandLinePattern = regexp.MustCompile(`relevo ([a-z-]+)((?: --?[a-z][a-z-]*)+)`)

// backtickSpanPattern finds inline code spans in a markdown line: relevo's
// docs mark every runnable command line this way, which is what tells a
// command example ("`relevo bind --worktree`") from a mention of the tool by
// name in prose ("relevo identifies this session itself").
var backtickSpanPattern = regexp.MustCompile("`([^`]*)`")

// c12Sources lists every file this contract scans.
func c12Sources(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, pattern := range []string{
		filepath.Join("..", "..", "internal", "harness", "agents", "*.md"),
		filepath.Join("..", "..", "claude-plugin", "commands", "*.md"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("glob %s matched nothing", pattern)
		}
		files = append(files, matches...)
	}
	files = append(files,
		filepath.Join("..", "..", "internal", "mcp", "instructions.go"),
		filepath.Join("..", "..", "internal", "delivery", "push.go"),
	)
	return files
}

// c12FlagSets maps a verb to a function that installs its flags on a fresh
// FlagSet, for every verb whose flags a test can reach today (found by
// grepping cmd/relevo/*.go for `func \w+FlagSet\(`). show, status, history,
// wait, gate, config and mastermind build their flags inline in their cmd
// function, so only the verb itself is checked for them below.
func c12FlagSets() map[string]func(*flag.FlagSet) {
	return map[string]func(*flag.FlagSet){
		"bind": func(fs *flag.FlagSet) { bindFlagSet(fs) },
	}
}

// checkCommandLineMatch validates one relevoCommandLinePattern match against
// verbs and, where the verb's flags are reachable, flagSets, reporting any
// failure against path/line.
func checkCommandLineMatch(t *testing.T, path string, line int, m []string, verbs map[string]bool, flagSets map[string]func(*flag.FlagSet)) {
	t.Helper()
	verb, flagsPart := m[1], m[2]
	if !verbs[verb] {
		t.Errorf("%s:%d: %q names verb %q, which relevo does not dispatch", path, line, m[0], verb)
		return
	}
	install, reachable := flagSets[verb]
	if !reachable {
		return
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	install(fs)
	for _, tok := range strings.Fields(flagsPart) {
		name := strings.TrimLeft(tok, "-")
		if fs.Lookup(name) == nil {
			t.Errorf("%s:%d: %q uses --%s, which %s's flag set does not define", path, line, m[0], name, verb)
		}
	}
}

// TestContractCommandLineReferences pins C12: every `relevo <verb> [--flag
// ...]` line in the four listed sources names a verb relevo actually
// dispatches, and, where the verb's flags are reachable from a test, every
// flag on that line is one the verb's FlagSet defines. A failing case names
// the file and line rather than a golden -- the contract is "every reference
// still resolves", not one fixed rendering.
//
// A markdown file's command examples are backtick-quoted (its docs'
// convention throughout this repo); the pattern runs only inside those
// spans, so a prose sentence that happens to name the tool ("relevo
// identifies this session itself") is not mistaken for one. The two Go
// sources carry their examples as plain sentences with no such marker, so
// relevoFlaggedCommandLinePattern (a mandatory flag) stands in for it there --
// see its doc comment.
func TestContractCommandLineReferences(t *testing.T) {
	verbs := commandVerbs(t)
	flagSets := c12FlagSets()

	for _, path := range c12Sources(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		markdown := strings.HasSuffix(path, ".md")
		for i, line := range strings.Split(string(raw), "\n") {
			if markdown {
				for _, span := range backtickSpanPattern.FindAllStringSubmatch(line, -1) {
					for _, m := range relevoCommandLinePattern.FindAllStringSubmatch(span[1], -1) {
						checkCommandLineMatch(t, path, i+1, m, verbs, flagSets)
					}
				}
				continue
			}
			for _, m := range relevoFlaggedCommandLinePattern.FindAllStringSubmatch(line, -1) {
				checkCommandLineMatch(t, path, i+1, m, verbs, flagSets)
			}
		}
	}
}

// TestHelpJSONDocumentsTheSurface pins the whole registry document: one JSON
// line on stdout, nothing on stderr, every dispatched verb present, and the
// error catalog beside them. The build stamp is normalized, so the golden
// pins the shape rather than one build.
func TestHelpJSONDocumentsTheSurface(t *testing.T) {
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"help", "--json"})
	})
	if err != nil {
		t.Fatalf("help --json: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("help --json: stderr = %q, want empty", stderr)
	}
	assertGolden(t, "help-json", normalize(stdout))
}

// TestHelpJSONOneVerb pins the one-verb form with a name that holds a space,
// so the multi-word form is covered by the golden.
func TestHelpJSONOneVerb(t *testing.T) {
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"help", "--json", "config", "server", "add"})
	})
	if err != nil {
		t.Fatalf("help --json config server add: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("help --json config server add: stderr = %q, want empty", stderr)
	}
	assertGolden(t, "help-json-verb", normalize(stdout))
}

// TestHelpJSONUnknownVerbRefused pins the refusal: the usage code and the
// next command, with nothing written before report renders it.
func TestHelpJSONUnknownVerbRefused(t *testing.T) {
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"help", "--json", "bogus"})
	})
	if len(stdout) != 0 {
		t.Errorf("help --json bogus: stdout = %q, want empty", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("help --json bogus: stderr = %q, want empty before report", stderr)
	}
	requireCLIError(t, err, codeUsage, "relevo help")
}
