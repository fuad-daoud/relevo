package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// collectQuery runs stmt through QueryReadOnly and collects what it streamed,
// so a test can assert the seam's own output rather than a caller's reading of
// it.
func collectQuery(t *testing.T, d *DB, stmt string) (columns []string, rows [][]any, err error) {
	t.Helper()
	err = d.QueryReadOnly(context.Background(), stmt, func(cols []string, values []any) error {
		columns = cols
		rows = append(rows, values)
		return nil
	})
	return columns, rows, err
}

// noQueryRows is the callback for a statement whose rows do not matter.
func noQueryRows([]string, []any) error { return nil }

func TestQueryReadOnlyStreamsRows(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.sqlDB.Exec(`CREATE TABLE probe (n INTEGER, label TEXT)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO probe (n, label) VALUES (1, 'one')`,
		`INSERT INTO probe (n, label) VALUES (2, 'two')`,
	} {
		if _, err := d.sqlDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	columns, rows, err := collectQuery(t, d, `SELECT n, label FROM probe ORDER BY n`)
	if err != nil {
		t.Fatalf("QueryReadOnly: %v", err)
	}
	if !reflect.DeepEqual(columns, []string{"n", "label"}) {
		t.Errorf("columns = %v, want [n label]", columns)
	}
	want := [][]any{{int64(1), "one"}, {int64(2), "two"}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v, want %v", rows, want)
	}
}

// TestQueryReadOnlyRefusesAnInsert writes through a CTE, so the statement's
// first keyword (WITH) is one the read-only allowlist carries: the refusal this
// pins is the engine's query_only, not the allowlist.
func TestQueryReadOnlyRefusesAnInsert(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.sqlDB.Exec(`CREATE TABLE probe (n INTEGER)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}

	err := d.QueryReadOnly(context.Background(), `WITH x AS (SELECT 1 AS n) INSERT INTO probe (n) SELECT n FROM x`, noQueryRows)
	if err == nil {
		t.Fatal("a CTE-wrapped INSERT was not refused")
	}
	t.Logf("refused with: %v", err)

	var count int
	if qerr := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM probe`).Scan(&count); qerr != nil {
		t.Fatalf("count probe: %v", qerr)
	}
	if count != 0 {
		t.Errorf("probe holds %d rows after a refused insert, want 0", count)
	}
}

func TestQueryReadOnlyLeavesAWritingPragmaWithoutEffect(t *testing.T) {
	d := openTestDB(t)

	// A writing PRAGMA is not on Turso's query_only list, so the transaction --
	// not the engine -- is what has to undo it. Either outcome is fine here:
	// the assertion is that the value never moved.
	_ = d.QueryReadOnly(context.Background(), `PRAGMA user_version = 7`, noQueryRows)

	var version int
	if err := d.sqlDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 0 {
		t.Errorf("user_version = %d after a rolled-back write, want 0", version)
	}
}

func TestQueryReadOnlyRefusesTwoStatements(t *testing.T) {
	d := openTestDB(t)

	err := d.QueryReadOnly(context.Background(), `SELECT 1; SELECT 2`, noQueryRows)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("two statements: err = %v, want ErrInvalid", err)
	}
}

// TestQueryReadOnlyRefusesAttach pins the allowlist's own job: query_only does
// not block ATTACH, which creates a missing file, so only the first-keyword
// check keeps it out.
func TestQueryReadOnlyRefusesAttach(t *testing.T) {
	d := openTestDB(t)
	target := filepath.Join(t.TempDir(), "attached.db")

	err := d.QueryReadOnly(context.Background(), `ATTACH DATABASE '`+target+`' AS extra`, noQueryRows)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ATTACH: err = %v, want ErrInvalid", err)
	}
	if _, serr := os.Stat(target); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("ATTACH created %s: stat error = %v, want not-exist", target, serr)
	}
}

// TestSingleStatementIgnoresSemicolonsInStringsAndComments pins the lexer: only
// a top-level semicolon separates statements.
func TestSingleStatementIgnoresSemicolonsInStringsAndComments(t *testing.T) {
	cases := []struct {
		name string
		stmt string
		ok   bool
	}{
		{"plain", `SELECT 1`, true},
		{"trailing semicolon", `SELECT 1;`, true},
		{"semicolon in a string", `SELECT ';'`, true},
		{"semicolon in an identifier", `SELECT ";"`, true},
		{"semicolon in a bracket identifier", `SELECT [;]`, true},
		{"semicolon in a backtick identifier", "SELECT `;`", true},
		{"line comment", "SELECT 1 -- ; not a statement", true},
		{"block comment", `SELECT 1 /* ; not a statement */`, true},
		{"doubled quote in a string", `SELECT 'a'';b'`, true},
		{"two statements", `SELECT 1; SELECT 2`, false},
		{"empty statement", `;`, false},
		{"blank", "   ", false},
		{"two trailing semicolons", `SELECT 1;;`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := singleStatement(c.stmt)
			if c.ok && err != nil {
				t.Errorf("singleStatement(%q) = %v, want one statement", c.stmt, err)
			}
			if !c.ok && err == nil {
				t.Errorf("singleStatement(%q) accepted a malformed statement", c.stmt)
			}
			if !c.ok && err != nil && !errors.Is(err, ErrInvalid) {
				t.Errorf("singleStatement(%q) = %v, want ErrInvalid", c.stmt, err)
			}
		})
	}
}

// TestQueryReadOnlyLeavesNoQueryOnlyConnectionInThePool pins the discard: the
// pool holds one connection at most, so a write after a read lands on a fresh
// connection only if the read-only one left the pool.
func TestQueryReadOnlyLeavesNoQueryOnlyConnectionInThePool(t *testing.T) {
	d := directOpenTestDB(t)
	d.sqlDB.SetMaxOpenConns(1)
	d.sqlDB.SetMaxIdleConns(1)

	if err := d.QueryReadOnly(context.Background(), `SELECT 1`, noQueryRows); err != nil {
		t.Fatalf("QueryReadOnly: %v", err)
	}
	if err := d.KVPut("after-query", []byte(`1`)); err != nil {
		t.Fatalf("a write after a read-only query must succeed on a fresh connection: %v", err)
	}
}
