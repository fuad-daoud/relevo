package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// seedQueryRoot points the state root at a fresh temp directory and writes a
// relevo.db there with one probe table of three rows: an integer, a text and a
// blob column, with one NULL and one blob value. It returns the state home, so
// a test that needs an owner beside it can serve one there.
func seedQueryRoot(t *testing.T) string {
	t.Helper()
	return seedQueryRootAt(t, t.TempDir())
}

// seedQueryRootAt seeds the probe database under an existing state home and
// points the environment at it. The caller chooses the directory so a test that
// serves an owner beside the database can keep the socket path inside
// sun_path's limit, which t.TempDir is too long for on macOS.
func seedQueryRootAt(t *testing.T, stateHome string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateHome, "config"))

	path := filepath.Join(stateHome, "relevo", "relevo.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	raw := dbtest.RawOpen(t, path)
	if _, err := raw.Exec(`CREATE TABLE probe (n INTEGER, label TEXT, data BLOB)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO probe (n, label, data) VALUES (1, 'one', NULL)`); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO probe (n, label, data) VALUES (2, 'two', ?)`, []byte{0, 1, 2}); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return stateHome
}

func TestDBQueryPrintsATableFromADirectOpen(t *testing.T) {
	seedQueryRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT n, label, data FROM probe ORDER BY n`})
	})
	if err != nil {
		t.Fatalf("db query: %v (stderr: %s)", err, stderr)
	}
	want := "n  label  data\n1  one    NULL\n2  two    <blob 3 bytes>\n"
	if string(stdout) != want {
		t.Errorf("table =\n%q\nwant\n%q", stdout, want)
	}
}

func TestDBQueryJSONIsColumnsAndRows(t *testing.T) {
	seedQueryRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT n, label FROM probe ORDER BY n`, "--json"})
	})
	if err != nil {
		t.Fatalf("db query --json: %v (stderr: %s)", err, stderr)
	}
	var doc struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if len(doc.Columns) != 2 || doc.Columns[0] != "n" || doc.Columns[1] != "label" {
		t.Errorf("columns = %v, want [n label]", doc.Columns)
	}
	// encoding/json decodes a JSON number into a float64, which is what a row
	// value arrives as here.
	want := [][]any{{float64(1), "one"}, {float64(2), "two"}}
	if len(doc.Rows) != len(want) || doc.Rows[0][0] != want[0][0] || doc.Rows[0][1] != want[0][1] ||
		doc.Rows[1][0] != want[1][0] || doc.Rows[1][1] != want[1][1] {
		t.Errorf("rows = %v, want %v", doc.Rows, want)
	}
}

func TestDBQueryRefusesAWriteWithCodeRefused(t *testing.T) {
	seedQueryRoot(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `INSERT INTO probe (n) VALUES (9)`})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("run = %v, want exit code 2", err)
	}
	if !strings.Contains(ce.message, "read-only") {
		t.Errorf("message = %q, want the read-only seam's refusal", ce.message)
	}
}

func TestDBQueryWithoutSQLIsUsage(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query"})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestDBQueryTwoStatementsIsUsage pins the shape rejection: one argument
// holding two statements is a malformed argument, not a statement the engine
// refused.
func TestDBQueryTwoStatementsIsUsage(t *testing.T) {
	seedQueryRoot(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT 1; SELECT 2`})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestDBQueryRecursiveIsUsage pins the verb's RECURSIVE limit: the statement is
// a malformed argument for this verb, not a statement the engine refused.
func TestDBQueryRecursiveIsUsage(t *testing.T) {
	seedQueryRoot(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `WITH RECURSIVE c(x) AS (SELECT 1) SELECT x FROM c`})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestDBQueryPragmaAssignmentIsUsage pins the narrowed PRAGMA allowance: the
// writing form is a malformed argument, not a statement the engine refused.
func TestDBQueryPragmaAssignmentIsUsage(t *testing.T) {
	seedQueryRoot(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `PRAGMA user_version = 7`})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestDBQueryUnlistedPragmaIsUsage pins the list itself: a pragma in the read
// form is still a usage error when its name is not one db query accepts.
func TestDBQueryUnlistedPragmaIsUsage(t *testing.T) {
	seedQueryRoot(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `PRAGMA writable_schema`})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestDBQueryRunsAListedPragma pins the accepted side of the allowance.
func TestDBQueryRunsAListedPragma(t *testing.T) {
	seedQueryRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `PRAGMA table_info(probe)`})
	})
	if err != nil {
		t.Fatalf("db query of a listed pragma: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "label") {
		t.Errorf("table_info output = %q, want the probe columns", stdout)
	}
}

// TestDBQueryDeadlineRefusesADirectReadThatIsAlreadyPast pins the deadline's
// own refusal: a budget that is already spent when the verb starts ends the
// command with a refused timeout, not a direct read.
func TestDBQueryDeadlineRefusesADirectReadThatIsAlreadyPast(t *testing.T) {
	seedQueryRoot(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT 1`, "--timeout", "1ns"})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(ce.message, "did not finish within") {
		t.Errorf("message = %q, want the deadline refusal", ce.message)
	}
}

// TestDBQueryDeadlineAbandonsAReadTheEngineIgnores pins that the command is not
// hostage to a read that never checks ctx: the read blocks on a channel the test
// holds, and the deadline must still end the command. The mutation to a
// context.Background() read makes this hang.
func TestDBQueryDeadlineAbandonsAReadTheEngineIgnores(t *testing.T) {
	seedQueryRoot(t)

	release := make(chan struct{})
	finished := make(chan struct{})
	prev := dbQueryRead
	dbQueryRead = func(context.Context, *db.DB, string, func([]string, []any) error) error {
		<-release
		close(finished)
		return nil
	}
	t.Cleanup(func() {
		close(release)
		<-finished
		dbQueryRead = prev
	})

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT 1`, "--timeout", "100ms"})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(ce.message, "did not finish within") {
		t.Errorf("message = %q, want the deadline refusal", ce.message)
	}
}

// TestDBQueryRowLimitPrintsWhatFitsAndSaysTruncated pins the row cap: the rows
// that fit are printed, one extra row is consumed only to prove the cut, and
// the truncation is noted on stderr while stdout stays a valid document.
func TestDBQueryRowLimitPrintsWhatFitsAndSaysTruncated(t *testing.T) {
	seedQueryRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT n, label FROM probe ORDER BY n`, "--limit", "1"})
	})
	if err != nil {
		t.Fatalf("db query --limit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "one") || strings.Contains(string(stdout), "two") {
		t.Errorf("stdout = %q, want the first row only", stdout)
	}
	if !strings.Contains(string(stderr), "truncated at 1 rows (raise --limit)") {
		t.Errorf("stderr = %q, want the truncation note", stderr)
	}

	jsonOut, _, jerr := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT n, label FROM probe ORDER BY n`, "--limit", "1", "--json"})
	})
	if jerr != nil {
		t.Fatalf("db query --json --limit: %v", jerr)
	}
	var doc struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if uerr := json.Unmarshal(jsonOut, &doc); uerr != nil {
		t.Fatalf("truncated --json is not valid JSON: %v (%q)", uerr, jsonOut)
	}
	if len(doc.Rows) != 1 {
		t.Errorf("truncated --json rows = %d, want 1", len(doc.Rows))
	}
}

// TestDBQueryByteCapPrintsWhatFitsAndSaysTruncated pins the byte cap: rows
// whose values fit are printed, the row that would pass the budget is not, and
// the truncation is noted on stderr while stdout stays a valid table.
func TestDBQueryByteCapPrintsWhatFitsAndSaysTruncated(t *testing.T) {
	seedQueryRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{
			"db", "query",
			`SELECT zeroblob(300) AS b FROM (SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3)`,
			"--max-bytes", "700",
		})
	})
	if err != nil {
		t.Fatalf("db query --max-bytes: %v (stderr: %s)", err, stderr)
	}
	if got := strings.Count(string(stdout), "<blob 300 bytes>"); got != 2 {
		t.Errorf("stdout has %d blob rows, want 2:\n%s", got, stdout)
	}
	if !strings.Contains(string(stderr), "truncated at 700 bytes (raise --max-bytes)") {
		t.Errorf("stderr = %q, want the byte truncation note", stderr)
	}
}

// TestDBQueryByteCapStopsAValueOverTheBudget pins that one value larger than
// the whole budget is never printed: the row is not appended, the read stops,
// and the command still exits 0 with a valid document.
func TestDBQueryByteCapStopsAValueOverTheBudget(t *testing.T) {
	seedQueryRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT zeroblob(1000000) AS b`, "--max-bytes", "1024"})
	})
	if err != nil {
		t.Fatalf("db query --max-bytes: %v (stderr: %s)", err, stderr)
	}
	if strings.Contains(string(stdout), "<blob") {
		t.Errorf("stdout printed an over-budget value:\n%s", stdout)
	}
	if !strings.Contains(string(stderr), "truncated at 1024 bytes (raise --max-bytes)") {
		t.Errorf("stderr = %q, want the byte truncation note", stderr)
	}
}

// TestDBQueryMaxBytesFlagBounds pins the flag's bounds: a byte cap below one is
// a usage error, and so is one above the owner's ad-hoc value ceiling, which no
// ad-hoc read may exceed.
func TestDBQueryMaxBytesFlagBounds(t *testing.T) {
	seedQueryRoot(t)

	for _, args := range [][]string{
		{"db", "query", `SELECT 1`, "--max-bytes", "0"},
		{"db", "query", `SELECT 1`, "--max-bytes", "-1"},
		{"db", "query", `SELECT 1`, "--max-bytes", strconv.Itoa(wire.AdHocReadCeiling + 1)},
	} {
		_, _, err := captureOutput(t, func() error { return run(args) })
		requireCLIError(t, err, codeUsage, "relevo help")
	}
}

// TestDBQueryReleasesTheLockBeforePrinting pins the print-phase lock: the
// handle is closed before any row or note goes out, so a stalled stdout cannot
// hold relevo.db.lock past the read phase. While the writer blocks, the lock
// file must be free. The mutation to a deferred close makes the flock fail.
func TestDBQueryReleasesTheLockBeforePrinting(t *testing.T) {
	stateHome := seedQueryRoot(t)

	w := newBlockingWriter()
	prev := dbQueryOut
	dbQueryOut = w
	t.Cleanup(func() { dbQueryOut = prev })

	done := make(chan error, 1)
	go func() {
		done <- run([]string{"db", "query", `SELECT n FROM probe ORDER BY n`})
	}()

	select {
	case <-w.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the command never reached its print")
	}

	lockPath := filepath.Join(stateHome, "relevo", "relevo.db.lock")
	if !flockIsFree(lockPath) {
		t.Error("relevo.db.lock is still held while stdout is stalled")
	}

	w.unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("db query: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command never finished after the print was released")
	}
}

// blockingWriter signals when its first write starts and blocks until released,
// so a test can act while the command is printing.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (w *blockingWriter) unblock() { close(w.release) }

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

// TestDBQueryNotConvertedRefusalCarriesTheDaemonHint pins the folded cause: the
// ErrNotConverted refusal ends with the daemon hint rather than only naming the
// file it could not read.
func TestDBQueryNotConvertedRefusalCarriesTheDaemonHint(t *testing.T) {
	seedQueryRoot(t)

	prev := openReadOnlyDB
	openReadOnlyDB = func(string, db.Options) (*db.DB, error) { return nil, db.ErrNotConverted }
	t.Cleanup(func() { openReadOnlyDB = prev })

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT 1`})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(ce.message, "start the daemon once") {
		t.Errorf("message = %q, want the daemon hint", ce.message)
	}
}

// TestDBQueryConflictNamesTheLockNotTheDaemon pins the conflict message: when
// the file is held and no owner answers, the message names the lock and the
// socket, never the daemon that may not exist.
func TestDBQueryConflictNamesTheLockNotTheDaemon(t *testing.T) {
	seedQueryRoot(t)

	prev := openReadOnlyDB
	openReadOnlyDB = func(string, db.Options) (*db.DB, error) { return nil, db.ErrLocked }
	t.Cleanup(func() { openReadOnlyDB = prev })

	_, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT 1`})
	})
	ce := requireCLIError(t, err, codeConflict, "")
	if strings.Contains(ce.message, "daemon") {
		t.Errorf("message = %q, want it not to blame the daemon", ce.message)
	}
	if !strings.Contains(ce.message, "relevo.db.lock") {
		t.Errorf("message = %q, want it to name the lock", ce.message)
	}
	if !strings.Contains(ce.message, "relevo.sock") {
		t.Errorf("message = %q, want it to name the unanswered socket", ce.message)
	}
}

// TestDBQueryTimeoutFlagIsCappedBelowTheOpenLockWait pins the flag's bound: a
// deadline above the read-only hold budget is a usage error, and the budget
// itself is accepted.
func TestDBQueryTimeoutFlagIsCappedBelowTheOpenLockWait(t *testing.T) {
	seedQueryRoot(t)

	for _, args := range [][]string{
		{"db", "query", `SELECT 1`, "--timeout", "13s"},
		{"db", "query", `SELECT 1`, "--timeout", "15s"},
		{"db", "query", `SELECT 1`, "--timeout", "1m"},
		{"db", "query", `SELECT 1`, "--timeout", "0s"},
		{"db", "query", `SELECT 1`, "--limit", "0"},
	} {
		_, _, err := captureOutput(t, func() error { return run(args) })
		requireCLIError(t, err, codeUsage, "relevo help")
	}

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT 1`, "--timeout", "12s"})
	}); err != nil {
		t.Errorf("--timeout 12s = %v, want it accepted", err)
	}
}

// TestDBQueryTableSanitisesHeaderAndCells pins the terminal boundary: an ESC in
// a value or in a quoted column alias is replaced with U+FFFD in the table, so a
// stored string cannot drive the terminal, while --json keeps the raw value.
func TestDBQueryTableSanitisesHeaderAndCells(t *testing.T) {
	seedQueryRoot(t)

	stmt := "SELECT char(27)||'[31mRED' AS c, 1 AS \"\x1b[31mH\""

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", stmt})
	})
	if err != nil {
		t.Fatalf("db query: %v (stderr: %s)", err, stderr)
	}
	if strings.ContainsRune(string(stdout), '\x1b') {
		t.Errorf("table stdout still holds an ESC: %q", stdout)
	}
	if !strings.ContainsRune(string(stdout), '\uFFFD') {
		t.Errorf("table stdout = %q, want the U+FFFD replacement", stdout)
	}

	jsonOut, _, jerr := captureOutput(t, func() error {
		return run([]string{"db", "query", stmt, "--json"})
	})
	if jerr != nil {
		t.Fatalf("db query --json: %v", jerr)
	}
	if !strings.Contains(string(jsonOut), `\u001b`) {
		t.Errorf("--json stdout = %q, want the raw ESC kept in the machine form", jsonOut)
	}
}

// TestDBIsNoLongerARetiredVerb pins that `db` is dispatched now: it is not in
// the retired-verb map, and a bare `relevo db` prints the usage line for its
// own subcommand rather than the removal notice.
func TestDBIsNoLongerARetiredVerb(t *testing.T) {
	if _, ok := removedVerbs["db"]; ok {
		t.Error("the retired-verb map still holds \"db\"")
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db"})
	})
	if !errors.Is(err, errUsagePrinted) {
		t.Fatalf("run(db) = %v, want errUsagePrinted", err)
	}
	if len(stdout) != 0 {
		t.Errorf("run(db) wrote %q to stdout, want nothing", stdout)
	}
	if !strings.Contains(string(stderr), "relevo db query") {
		t.Errorf("run(db) stderr = %q, want the db usage line", stderr)
	}
}
