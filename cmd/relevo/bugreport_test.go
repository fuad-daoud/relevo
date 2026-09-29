package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/bugreport"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// datedBundleName is the default bundle's file name: a UTC timestamp, so each
// run writes a new file and no report is ever overwritten.
var datedBundleName = regexp.MustCompile(`^bugreport-\d{8}T\d{6}Z\.md$`)

// dbFacts is one database file as the read-only proof compares it.
type dbFacts struct {
	modTime time.Time
	size    int64
}

// dbFactsOf reads the two facts the read-only proof compares.
func dbFactsOf(t *testing.T, path string) dbFacts {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return dbFacts{modTime: fi.ModTime(), size: fi.Size()}
}

// filesUnder lists every file under root, so a pass that leaves a new one is
// visible.
func filesUnder(t *testing.T, root string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found[path] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// collectReadOnly makes every read a bundle's sources make, through a runtime
// built by newRuntimeReadOnly, and returns whether it could. It is the read
// half of a collect without the bundle around it, so the proof does not depend
// on the verb's own output.
func collectReadOnly(t *testing.T, root string) {
	t.Helper()

	rt, _, err := newRuntimeReadOnly()
	if err != nil {
		t.Fatalf("newRuntimeReadOnly: %v", err)
	}
	if rt.DB == nil {
		t.Fatal("read-only runtime carries no open database")
	}
	defer func() { _ = rt.DB.Close() }()

	if _, err := relevo.Status(context.Background(), rt); err != nil {
		t.Errorf("Status: %v", err)
	}
	bindings, err := rt.Store.List()
	if err != nil {
		t.Errorf("List: %v", err)
	}
	// The plan's read half: the read-only store must see the machine's
	// bindings. It does, because the handle carries the installation's id as
	// its origin, which is what every binding row is scoped to.
	if len(bindings) == 0 {
		t.Errorf("the read-only store listed no binding: the handle must carry the installation's origin for origin-scoped rows to match")
	}
	for _, b := range bindings {
		if _, err := rt.Store.ReadLog(b.Name); err != nil {
			t.Errorf("ReadLog %s: %v", b.Name, err)
		}
	}
	if _, err := hooks.NewKVLog(db.TxKV{DB: rt.DB}).Runs(); err != nil {
		t.Errorf("hook runs: %v", err)
	}
	if _, err := availability.LoadLedger(rt.Gates); err != nil {
		t.Errorf("LoadLedger: %v", err)
	}
	_ = availability.Gates(relevo.AvailabilityDeps(rt))
	if _, err := rt.Store.DaemonRunning(); err != nil {
		t.Errorf("DaemonRunning: %v", err)
	}
	if _, _, err := rt.Store.ReadDaemonInfo(); err != nil {
		t.Errorf("ReadDaemonInfo: %v", err)
	}

	// A write is refused at the SQLite level rather than applied.
	if err := rt.Store.Save(store.Binding{
		Name: "beta", CWD: filepath.Join(root, "work"), Round: 1, State: store.StateActive,
	}); err == nil {
		t.Error("Save through the read-only runtime succeeded, want the SQLite failure")
	}
}

// TestReadOnlyRuntimeWritesNothing pins the bundle's read-only contract: a
// collect leaves the machine database's mtime and size exactly as they were,
// and adds no file to the state root. The fixture collects once to settle the
// root with the artifacts the read path inherently opens -- the daemon lock
// and the database's WAL sidecars -- so the collect under test is compared
// against a machine that already carries them and a bundle is the only file a
// run may ever add. This pass writes none, so the assertion is that nothing is
// new at all.
func TestReadOnlyRuntimeWritesNothing(t *testing.T) {
	docsEnv(t)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	seed := store.New(root)
	if err := seed.Save(store.Binding{
		Name: "alpha", CWD: filepath.Join(root, "work"), Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// Settle the seeding handle first: the proof is about the read-only pass,
	// not about the writes the fixture makes to build a machine.
	if d, derr := seed.DB(); derr == nil {
		if cerr := d.Close(); cerr != nil {
			t.Fatalf("close seeded db: %v", cerr)
		}
	}

	collectReadOnly(t, root)
	before := dbFactsOf(t, seed.DBPath())
	beforeFiles := filesUnder(t, root)

	collectReadOnly(t, root)

	if after := dbFactsOf(t, seed.DBPath()); after != before {
		t.Errorf("database changed: %+v before, %+v after", before, after)
	}
	for p := range filesUnder(t, root) {
		if !beforeFiles[p] {
			t.Errorf("new file under the state root: %s", p)
		}
	}
}

// TestBugreportNeverCaptures pins the peek rule for the read-only verb: with a
// capturable agy environment present and no machine database on the root, a
// `bugreport --stdout` run leaves no database behind. captureAgyEnv opens -- and
// can migrate -- the database the moment there is something to capture, so the
// absence of relevo.db (and its sidecars and the installation file) is the proof
// the verb skipped capture.
func TestBugreportNeverCaptures(t *testing.T) {
	docsEnv(t)
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "0f0e0d0c-0b0a-4998-8877-665544332211")
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "localhost:42139")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "the-csrf-token")
	fakeBugreportExec(t, nil, nil)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "relevo.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture root already carries a database: %v", err)
	}

	if _, stderr, err := captureOutput(t, func() error { return run([]string{"bugreport", "--stdout"}) }); err != nil {
		t.Fatalf("bugreport --stdout: %v (stderr: %s)", err, stderr)
	}

	for _, name := range []string{"relevo.db", "relevo.db-wal", "relevo.db-shm", "installation.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after bugreport: stat error = %v, want not-exist", name, err)
		}
	}
}

// seedBugreportMachine writes the state root and one binding the bugreport
// cases read, so a bundle has a machine to report on rather than an empty
// root.
func seedBugreportMachine(t *testing.T, name string) string {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	st := store.New(root)
	if err := st.Save(store.Binding{
		Name: name, CWD: filepath.Join(root, "work"), Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if d, derr := st.DB(); derr == nil {
		if cerr := d.Close(); cerr != nil {
			t.Fatalf("close seeded db: %v", cerr)
		}
	}
	return root
}

// fakeBugreportExec replaces the verb's exec seam and records every argv it
// was asked to run, so no test spawns gh or journalctl.
func fakeBugreportExec(t *testing.T, out []byte, err error) *[][]string {
	t.Helper()

	var calls [][]string
	orig := bugreportExec
	bugreportExec = func(_ context.Context, argv []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), argv...))
		return out, err
	}
	t.Cleanup(func() { bugreportExec = orig })
	return &calls
}

// bundleTitle is the title a fresh machine's bundle carries: no failure has
// been recorded, so it is the plain name.
func bundleTitle() string {
	return "relevo " + buildVersion() + ": bug report"
}

// TestBugreportDefaultWritesDatedFileAndPrintsGhLine pins the default run: a
// new dated file under <state>/bugreports/, mode 0600, and stdout's two lines
// -- the path, then the exact gh line the file would be filed with.
func TestBugreportDefaultWritesDatedFileAndPrintsGhLine(t *testing.T) {
	docsEnv(t)
	root := seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, nil)

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"bugreport"}) })
	if err != nil {
		t.Fatalf("bugreport: %v (stderr: %s)", err, stderr)
	}

	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want the path then the gh line", stdout)
	}
	path := lines[0]
	if got := filepath.Dir(path); got != filepath.Join(root, "bugreports") {
		t.Errorf("bundle dir = %q, want %q", got, filepath.Join(root, "bugreports"))
	}
	if !datedBundleName.MatchString(filepath.Base(path)) {
		t.Errorf("bundle name = %q, want bugreport-<UTC>.md", filepath.Base(path))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bundle: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode = %v, want 0600", fi.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if !strings.HasPrefix(string(body), "# "+bundleTitle()+"\n") {
		t.Errorf("bundle does not start with the title:\n%s", body)
	}

	want := bugreport.ShellLine(bugreport.IssueArgv(bundleTitle(), path))
	if lines[1] != want {
		t.Errorf("gh line = %q, want %q", lines[1], want)
	}
}

// TestBugreportOutWritesExactPath pins --out: the markdown lands at exactly
// the named path, with its parent directory created for it.
func TestBugreportOutWritesExactPath(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, nil)

	path := filepath.Join(t.TempDir(), "nested", "reports", "bundle.md")
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"bugreport", "--out", path})
	})
	if err != nil {
		t.Fatalf("bugreport --out: %v (stderr: %s)", err, stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("--out path: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 2 || lines[0] != path {
		t.Fatalf("stdout = %q, want the path %q then the gh line", stdout, path)
	}
	if want := bugreport.ShellLine(bugreport.IssueArgv(bundleTitle(), path)); lines[1] != want {
		t.Errorf("gh line = %q, want %q", lines[1], want)
	}
}

// TestBugreportOutUnwritableIsInternal pins the output failure: a path under
// a regular file cannot be created, and that is the verb's own failure with
// the internal code and exit 1.
func TestBugreportOutUnwritableIsInternal(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, nil)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	_, _, err := captureOutput(t, func() error {
		return run([]string{"bugreport", "--out", filepath.Join(blocker, "bundle.md")})
	})
	requireCLIError(t, err, codeInternal, "")
	if got := catalogExit(codeInternal); got != 1 {
		t.Errorf("internal exit = %d, want 1", got)
	}
}

// TestBugreportRoundRequiresName pins that a round is a round of one binding:
// --round alone is refused as usage before any runtime is built.
func TestBugreportRoundRequiresName(t *testing.T) {
	docsEnv(t)
	fakeBugreportExec(t, nil, nil)

	_, _, err := captureOutput(t, func() error { return run([]string{"bugreport", "--round", "2"}) })
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestBugreportStdoutAndJSONWriteNoFile pins the agent paths: --stdout prints
// the markdown and --json prints the document, and neither writes a file.
func TestBugreportStdoutAndJSONWriteNoFile(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{{"stdout", []string{"bugreport", "--stdout"}}, {"json", []string{"bugreport", "--json"}}} {
		t.Run(c.name, func(t *testing.T) {
			docsEnv(t)
			root := seedBugreportMachine(t, "alpha")
			fakeBugreportExec(t, nil, nil)

			stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
			if err != nil {
				t.Fatalf("%v: %v (stderr: %s)", c.args, err, stderr)
			}
			if _, err := os.Stat(filepath.Join(root, "bugreports")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%v wrote a file under bugreports (stat err = %v)", c.args, err)
			}
			if c.name == "json" {
				var doc bugreport.Doc
				if err := json.Unmarshal(stdout, &doc); err != nil {
					t.Fatalf("--json document: %v", err)
				}
				if doc.Title != bundleTitle() || doc.Version == "" || len(doc.Sections) == 0 {
					t.Errorf("--json document = %+v, want the bundle", doc)
				}
				return
			}
			if !strings.HasPrefix(string(stdout), "# "+bundleTitle()+"\n") {
				t.Errorf("--stdout = %q, want the markdown bundle", stdout)
			}
		})
	}
}

// TestBugreportGhRunsExactArgv pins --gh: the one argv the verb runs is the
// one the default prints, byte for byte.
func TestBugreportGhRunsExactArgv(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	calls := fakeBugreportExec(t, nil, nil)

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"bugreport", "--gh"}) })
	if err != nil {
		t.Fatalf("bugreport --gh: %v (stderr: %s)", err, stderr)
	}

	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("stdout = %q, want the path then the gh line", stdout)
	}
	path := lines[0]
	want := bugreport.IssueArgv(bundleTitle(), path)
	// The same seam reads the journal on Linux, so the gh run is the call
	// whose argv names gh -- and there must be exactly one.
	var gh [][]string
	for _, call := range *calls {
		if len(call) > 0 && call[0] == "gh" {
			gh = append(gh, call)
		}
	}
	if len(gh) != 1 {
		t.Fatalf("gh ran %d times, want exactly 1 (all calls: %v)", len(gh), *calls)
	}
	if !slices.Equal(gh[0], want) {
		t.Errorf("gh argv = %v, want %v", gh[0], want)
	}
	if line := bugreport.ShellLine(want); lines[1] != line {
		t.Errorf("printed line = %q, want %q", lines[1], line)
	}
}

// TestBugreportGhMissingIsNotAvailable pins the missing binary: filing is the
// only thing that failed, the code is not_available, and the message is the
// line a person can run by hand.
func TestBugreportGhMissingIsNotAvailable(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, &exec.Error{Name: "gh", Err: exec.ErrNotFound})

	stdout, _, err := captureOutput(t, func() error { return run([]string{"bugreport", "--gh"}) })
	ce := requireCLIError(t, err, codeNotAvailable, "")
	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("stdout = %q, want the path then the gh line", stdout)
	}
	if ce.message != lines[1] {
		t.Errorf("message = %q, want the printed line %q", ce.message, lines[1])
	}
}

// TestBugreportModesExclusive pins the flag contract: the three output modes
// pick one sink, --out is a path rather than a mode, and every illegal
// combination is usage, exit 2.
func TestBugreportModesExclusive(t *testing.T) {
	docsEnv(t)
	fakeBugreportExec(t, nil, nil)

	for _, args := range [][]string{
		{"bugreport", "--stdout", "--json"},
		{"bugreport", "--stdout", "--gh"},
		{"bugreport", "--json", "--gh"},
		{"bugreport", "--out", filepath.Join(t.TempDir(), "b.md"), "--stdout"},
		{"bugreport", "--out", filepath.Join(t.TempDir(), "b.md"), "--json"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(args) })
			requireCLIError(t, err, codeUsage, "relevo help")
			if got := catalogExit(codeUsage); got != 2 {
				t.Errorf("usage exit = %d, want 2", got)
			}
		})
	}
}

// TestBugreportJournalOmittedOffLinux pins the journal rule as a pure
// function of GOOS: a platform without the user unit says so in one line, and
// Linux reads the hundred lines the injected exec returns.
func TestBugreportJournalOmittedOffLinux(t *testing.T) {
	sec := journalSection("darwin", nil)
	if sec.Name != bugreport.SectionJournal || sec.Omitted != "not available on darwin" {
		t.Errorf("darwin journal = %+v, want the omitted line", sec)
	}
	if len(sec.Lines) != 0 {
		t.Errorf("darwin journal carries lines: %v", sec.Lines)
	}

	var ran [][]string
	sec = journalSection("linux", func(argv []string) ([]byte, error) {
		ran = append(ran, argv)
		return []byte("a\nb\n"), nil
	})
	if !slices.Equal(sec.Lines, []string{"a", "b"}) {
		t.Errorf("linux journal lines = %v, want the exec output", sec.Lines)
	}
	if len(ran) != 1 || !slices.Equal(ran[0], journalArgv) {
		t.Errorf("journal argv = %v, want %v", ran, journalArgv)
	}

	sec = journalSection("linux", func([]string) ([]byte, error) { return nil, errors.New("boom") })
	if sec.Omitted != "boom" {
		t.Errorf("failing journal = %+v, want the error as the omission", sec)
	}
}

// TestBugreportIsNotReport pins the name: `report` stays an unknown
// subcommand, and no registry entry claims it.
func TestBugreportIsNotReport(t *testing.T) {
	docsEnv(t)

	_, _, err := captureOutput(t, func() error { return run([]string{"report"}) })
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "unknown subcommand") {
		t.Errorf("message = %q, want the unknown-subcommand refusal", ce.message)
	}
	if _, ok := registryEntry("report"); ok {
		t.Error("the registry names a `report` entry, want none")
	}
}
