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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/bugreport"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/sanitize"
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

// captureBigOutput runs fn with stdout pointed at a file rather than a pipe.
// captureOutput reads its pipe only after fn returns, so a render larger than
// the pipe's buffer deadlocks; a capped-render test exceeds it.
func captureBigOutput(t *testing.T, fn func() error) ([]byte, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	runErr := fn()
	os.Stdout = orig
	if err := f.Close(); err != nil {
		t.Fatalf("close stdout file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stdout file: %v", err)
	}
	return data, runErr
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

// ghIssueURL is what a successful `gh issue create` prints on stdout: the new
// issue's URL, and nothing else.
const ghIssueURL = "https://github.com/fuad-daoud/relevo/issues/999"

// TestBugreportGhRunsExactArgv pins --gh: the one argv the verb runs is the
// one the default prints, byte for byte, and the issue URL gh answers with is
// printed after the command line.
func TestBugreportGhRunsExactArgv(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	calls := fakeBugreportExec(t, []byte(ghIssueURL+"\n"), nil)

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"bugreport", "--gh"}) })
	if err != nil {
		t.Fatalf("bugreport --gh: %v (stderr: %s)", err, stderr)
	}

	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want the path, the gh line and the issue URL", stdout)
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
	if lines[2] != ghIssueURL {
		t.Errorf("stdout does not end with the issue URL: third line = %q, want %q", lines[2], ghIssueURL)
	}
}

// TestBugreportGhFailureSurfacesTheReason pins the failed filing: gh's own
// refusal -- auth, permission, network -- is the user's environment, so it is
// not_available rather than internal, and the message carries gh's reason
// rather than a bare exit status.
func TestBugreportGhFailureSurfacesTheReason(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	reason := "gh: To use GitHub CLI, run gh auth login"
	fakeBugreportExec(t, []byte(reason+"\n"), errors.New("exit status 1"))

	_, _, err := captureOutput(t, func() error { return run([]string{"bugreport", "--gh"}) })
	ce := requireCLIError(t, err, codeNotAvailable, "")
	if ce.code == codeInternal {
		t.Errorf("code = %q, want not %q", ce.code, codeInternal)
	}
	if !strings.Contains(ce.message, reason) {
		t.Errorf("message = %q, want it to carry gh's reason %q", ce.message, reason)
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

// TestBundleIncludesLastError pins the error loop's read half: the failure the
// state root recorded reaches the bundle's Last error section, and its argv and
// message pass through the redaction rules like every other string.
func TestBundleIncludesLastError(t *testing.T) {
	docsEnv(t)
	root := seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, nil)

	secret := "ghp_16C7e42F292c6912E7710c838347Ae178B4a"
	if err := bugreport.WriteLastError(root, bugreport.LastError{
		Time:    time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Version: buildVersion(),
		Verb:    "status",
		Argv:    []string{"status", "--cwd", "/home/someone/work", secret},
		Code:    "internal",
		Message: "cannot read /home/someone/work " + secret,
		Next:    "relevo bugreport",
	}); err != nil {
		t.Fatalf("WriteLastError: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"bugreport", "--stdout"}) })
	if err != nil {
		t.Fatalf("bugreport --stdout: %v (stderr: %s)", err, stderr)
	}
	body := string(stdout)

	if !strings.HasPrefix(body, "# relevo "+buildVersion()+": internal in status\n") {
		t.Errorf("bundle title does not carry the recorded failure:\n%s", body)
	}
	for _, want := range []string{
		"| verb | status |",
		"| code | internal |",
		"| argv | status --cwd ~/work " + sanitize.Redacted + " |",
		"| message | cannot read ~/work " + sanitize.Redacted + " |",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bundle does not carry %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{secret, "/home/someone"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("bundle carries %q unredacted:\n%s", unwanted, body)
		}
	}
}

// TestBugreportTitleOverridesTheGeneratedTitle pins --title: a non-empty T
// replaces the generated title in the markdown header, the --json document, the
// printed line and the gh argv, and an empty T keeps the generated one.
func TestBugreportTitleOverridesTheGeneratedTitle(t *testing.T) {
	const title = "relevo: send hangs on a slow disk"

	t.Run("default", func(t *testing.T) {
		docsEnv(t)
		seedBugreportMachine(t, "alpha")
		fakeBugreportExec(t, nil, nil)

		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title})
		})
		if err != nil {
			t.Fatalf("bugreport --title: %v (stderr: %s)", err, stderr)
		}
		lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("stdout = %q, want the path then the gh line", stdout)
		}
		path := lines[0]
		if want := bugreport.ShellLine(bugreport.IssueArgv(title, path)); lines[1] != want {
			t.Errorf("printed line = %q, want %q", lines[1], want)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read bundle: %v", err)
		}
		if !strings.HasPrefix(string(body), "# "+title+"\n") {
			t.Errorf("bundle header does not carry the title:\n%s", body)
		}
	})

	t.Run("json", func(t *testing.T) {
		docsEnv(t)
		seedBugreportMachine(t, "alpha")
		fakeBugreportExec(t, nil, nil)

		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--json"})
		})
		if err != nil {
			t.Fatalf("bugreport --title --json: %v (stderr: %s)", err, stderr)
		}
		var doc bugreport.Doc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("--json document: %v", err)
		}
		if doc.Title != title {
			t.Errorf("document title = %q, want %q", doc.Title, title)
		}
	})

	t.Run("gh", func(t *testing.T) {
		docsEnv(t)
		seedBugreportMachine(t, "alpha")
		calls := fakeBugreportExec(t, []byte(ghIssueURL+"\n"), nil)

		if _, _, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--gh"})
		}); err != nil {
			t.Fatalf("bugreport --title --gh: %v", err)
		}
		var gh [][]string
		for _, call := range *calls {
			if len(call) > 0 && call[0] == "gh" {
				gh = append(gh, call)
			}
		}
		if len(gh) != 1 {
			t.Fatalf("gh ran %d times, want 1", len(gh))
		}
		if !slices.Contains(gh[0], title) {
			t.Errorf("gh argv = %v, want it to carry the title %q", gh[0], title)
		}
	})

	t.Run("empty", func(t *testing.T) {
		docsEnv(t)
		seedBugreportMachine(t, "alpha")
		fakeBugreportExec(t, nil, nil)

		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", "", "--stdout"})
		})
		if err != nil {
			t.Fatalf("bugreport --title '': %v (stderr: %s)", err, stderr)
		}
		if !strings.HasPrefix(string(stdout), "# "+bundleTitle()+"\n") {
			t.Errorf("an empty --title changed the generated title:\n%s", stdout)
		}
	})
}

// TestBugreportBodyBecomesTheFirstSection pins --body: the file's text is the
// first section in the markdown and the --json document, sanitized and redacted
// like every other section, and an empty file stays the heading with no lines.
func TestBugreportBodyBecomesTheFirstSection(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, nil)

	const token = "ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a"
	body := filepath.Join(t.TempDir(), "body.md")
	text := "the send hung\nwith a\ttab\nand a \x01 control\ntoken " + token + "\n"
	if err := os.WriteFile(body, []byte(text), 0o600); err != nil {
		t.Fatalf("write body: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"bugreport", "--body", body, "--stdout"})
	})
	if err != nil {
		t.Fatalf("bugreport --body: %v (stderr: %s)", err, stderr)
	}
	md := string(stdout)
	desc, env := strings.Index(md, "## Description"), strings.Index(md, "## Environment")
	if desc < 0 || env < 0 || desc > env {
		t.Errorf("Description is not the first section:\n%s", md)
	}
	if !strings.Contains(md, "with a    tab") {
		t.Errorf("the tab was not sanitized to four spaces:\n%s", md)
	}
	if strings.Contains(md, "\x01") || !strings.Contains(md, "\uFFFD") {
		t.Errorf("the control byte was not sanitized to U+FFFD:\n%s", md)
	}
	if strings.Contains(md, token) || !strings.Contains(md, sanitize.Redacted) {
		t.Errorf("the seeded token was not redacted:\n%s", md)
	}

	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"bugreport", "--body", body, "--json"})
	})
	if err != nil {
		t.Fatalf("bugreport --body --json: %v", err)
	}
	var doc bugreport.Doc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("--json document: %v", err)
	}
	if len(doc.Sections) == 0 || doc.Sections[0].Name != bugreport.SectionDescription {
		t.Errorf("sections = %+v, want Description first", doc.Sections)
	}

	empty := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty body: %v", err)
	}
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"bugreport", "--body", empty, "--stdout"})
	})
	if err != nil {
		t.Fatalf("bugreport --body <empty>: %v", err)
	}
	if !strings.Contains(string(stdout), "## Description\n\n\n## Environment") {
		t.Errorf("an empty description is not the heading with no lines:\n%s", stdout)
	}
}

// TestBugreportBodyUnreadableIsUsage pins the unreadable body: it is usage,
// exit 2, naming the path and the OS error, with nothing built, printed or
// written.
func TestBugreportBodyUnreadableIsUsage(t *testing.T) {
	docsEnv(t)
	fakeBugreportExec(t, nil, nil)

	missing := filepath.Join(t.TempDir(), "nope.md")
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"bugreport", "--body", missing})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, missing) {
		t.Errorf("message = %q, want the path %q", ce.message, missing)
	}
	if !strings.Contains(ce.message, "no such file or directory") {
		t.Errorf("message = %q, want the OS error", ce.message)
	}
	if got := catalogExit(codeUsage); got != 2 {
		t.Errorf("usage exit = %d, want 2", got)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want nothing printed", stdout)
	}
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bugreports")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a bugreports directory was created: %v", err)
	}
}

// TestBugreportBodyAndTitleCombineWithEveryMode pins that --title and --body
// combine with --stdout, --json, --gh, --out, --logs and --raw.
func TestBugreportBodyAndTitleCombineWithEveryMode(t *testing.T) {
	const title = "relevo: my own title"
	const description = "my own description"

	setup := func(t *testing.T) (string, string) {
		t.Helper()
		docsEnv(t)
		seedBugreportMachine(t, "alpha")
		body := filepath.Join(t.TempDir(), "body.md")
		if err := os.WriteFile(body, []byte(description+"\n"), 0o600); err != nil {
			t.Fatalf("write body: %v", err)
		}
		return body, filepath.Join(t.TempDir(), "bundle.md")
	}
	ghCall := func(t *testing.T, calls *[][]string) []string {
		t.Helper()
		for _, call := range *calls {
			if len(call) > 0 && call[0] == "gh" {
				return call
			}
		}
		t.Fatalf("no gh call in %v", *calls)
		return nil
	}

	t.Run("stdout", func(t *testing.T) {
		body, _ := setup(t)
		fakeBugreportExec(t, nil, nil)
		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--body", body, "--stdout"})
		})
		if err != nil {
			t.Fatalf("bugreport: %v (stderr: %s)", err, stderr)
		}
		if !strings.HasPrefix(string(stdout), "# "+title+"\n") || !strings.Contains(string(stdout), description) {
			t.Errorf("stdout does not carry the title and description:\n%s", stdout)
		}
	})

	t.Run("json", func(t *testing.T) {
		body, _ := setup(t)
		fakeBugreportExec(t, nil, nil)
		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--body", body, "--json"})
		})
		if err != nil {
			t.Fatalf("bugreport: %v (stderr: %s)", err, stderr)
		}
		var doc bugreport.Doc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("--json document: %v", err)
		}
		if doc.Title != title || len(doc.Sections) == 0 || doc.Sections[0].Name != bugreport.SectionDescription {
			t.Errorf("document = %+v, want the titled description first", doc)
		}
	})

	t.Run("gh", func(t *testing.T) {
		body, _ := setup(t)
		calls := fakeBugreportExec(t, []byte(ghIssueURL+"\n"), nil)
		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--body", body, "--gh"})
		})
		if err != nil {
			t.Fatalf("bugreport: %v (stderr: %s)", err, stderr)
		}
		path := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")[0]
		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read bundle: %v", err)
		}
		if !strings.HasPrefix(string(written), "# "+title+"\n") || !strings.Contains(string(written), description) {
			t.Errorf("the filed bundle does not carry the title and description:\n%s", written)
		}
		if call := ghCall(t, calls); !slices.Contains(call, title) {
			t.Errorf("gh argv = %v, want the title in it", call)
		}
	})

	t.Run("out", func(t *testing.T) {
		body, out := setup(t)
		fakeBugreportExec(t, nil, nil)
		if _, _, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--body", body, "--out", out})
		}); err != nil {
			t.Fatalf("bugreport: %v", err)
		}
		written, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("read --out bundle: %v", err)
		}
		if !strings.HasPrefix(string(written), "# "+title+"\n") || !strings.Contains(string(written), description) {
			t.Errorf("--out bundle does not carry the title and description:\n%s", written)
		}
	})

	t.Run("logs", func(t *testing.T) {
		body, _ := setup(t)
		fakeBugreportExec(t, nil, nil)
		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--body", body, "--logs", "--stdout"})
		})
		if err != nil {
			t.Fatalf("bugreport: %v (stderr: %s)", err, stderr)
		}
		if !strings.HasPrefix(string(stdout), "# "+title+"\n") || !strings.Contains(string(stdout), description) {
			t.Errorf("--logs stdout does not carry the title and description:\n%s", stdout)
		}
	})

	t.Run("raw", func(t *testing.T) {
		body, _ := setup(t)
		fakeBugreportExec(t, nil, nil)
		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"bugreport", "--title", title, "--body", body, "--raw", "--stdout"})
		})
		if err != nil {
			t.Fatalf("bugreport: %v (stderr: %s)", err, stderr)
		}
		if !strings.HasPrefix(string(stdout), "# "+title+"\n") || !strings.Contains(string(stdout), description) {
			t.Errorf("--raw stdout does not carry the title and description:\n%s", stdout)
		}
	})
}

// TestBugreportFileRenderIsCappedToGhLimit pins the file cap: a --body file
// over the limit yields a written file at or under the limit whose final line
// names the limit and --stdout, while --stdout still prints the full render.
func TestBugreportFileRenderIsCappedToGhLimit(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, nil)

	body := filepath.Join(t.TempDir(), "body.md")
	var sb strings.Builder
	line := strings.Repeat("d", 79) + "\n"
	for sb.Len() < 70<<10 {
		sb.WriteString(line)
	}
	if err := os.WriteFile(body, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write body: %v", err)
	}

	out := filepath.Join(t.TempDir(), "bundle.md")
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"bugreport", "--body", body, "--out", out})
	})
	if err != nil {
		t.Fatalf("bugreport --body --out: %v (stderr: %s)", err, stderr)
	}
	if len(stdout) == 0 {
		t.Error("the run printed neither the path nor the gh line")
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read capped bundle: %v", err)
	}
	if len(written) > bugreport.GhBodyLimit {
		t.Errorf("written file is %d bytes, want at most %d", len(written), bugreport.GhBodyLimit)
	}
	lines := strings.Split(strings.TrimRight(string(written), "\n"), "\n")
	final := lines[len(lines)-1]
	if !strings.Contains(final, strconv.Itoa(bugreport.GhBodyLimit)) || !strings.Contains(final, "--stdout") {
		t.Errorf("final line = %q, want it to name %d and --stdout", final, bugreport.GhBodyLimit)
	}

	stdout, err = captureBigOutput(t, func() error {
		return run([]string{"bugreport", "--body", body, "--stdout"})
	})
	if err != nil {
		t.Fatalf("bugreport --body --stdout: %v", err)
	}
	if len(stdout) <= bugreport.GhBodyLimit {
		t.Errorf("--stdout render is %d bytes, want the uncapped full render", len(stdout))
	}
	if !strings.Contains(string(stdout), sb.String()) {
		t.Error("--stdout is not the uncapped full text")
	}
}

// TestBugreportUncuttableBundleIsUsage pins the uncuttable bundle: a title
// whose first line cannot fit beside the cap marker is usage, exit 2, naming the
// size and the limit and writing no file, while --stdout still prints it.
func TestBugreportUncuttableBundleIsUsage(t *testing.T) {
	docsEnv(t)
	seedBugreportMachine(t, "alpha")
	fakeBugreportExec(t, nil, nil)

	title := strings.Repeat("t", 70000)
	out := filepath.Join(t.TempDir(), "bundle.md")
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"bugreport", "--title", title, "--out", out})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, strconv.Itoa(bugreport.GhBodyLimit)) {
		t.Errorf("message = %q, want the limit %d", ce.message, bugreport.GhBodyLimit)
	}
	if !regexp.MustCompile(`\d+ bytes`).MatchString(ce.message) {
		t.Errorf("message = %q, want the bundle's size", ce.message)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want nothing printed", stdout)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the uncuttable run wrote a file: %v", err)
	}

	stdout, err = captureBigOutput(t, func() error {
		return run([]string{"bugreport", "--title", title, "--stdout"})
	})
	if err != nil {
		t.Fatalf("bugreport --title --stdout: %v", err)
	}
	if !strings.HasPrefix(string(stdout), "# "+title+"\n") {
		t.Errorf("--stdout does not print the uncuttable bundle:\n%.80s", stdout)
	}
}
