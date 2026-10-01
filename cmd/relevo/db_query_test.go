package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

// seedQueryRoot points the state root at a fresh temp directory and writes a
// relevo.db there with one probe table of three rows: an integer, a text and a
// blob column, with one NULL and one blob value. It returns the state home, so
// a test that needs an owner beside it can serve one there.
func seedQueryRoot(t *testing.T) string {
	t.Helper()
	stateHome := t.TempDir()
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
