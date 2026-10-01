//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestTursoReadsAFileAfterRepairInvalidText pins the conversion repair end to
// end: invalid text is written with the modernc driver, repaired there, and the
// file is then opened by Turso, which refuses a database holding invalid UTF-8.
// The default build still links modernc for exactly this one-time conversion.
func TestTursoReadsAFileAfterRepairInvalidText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair.db")

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open modernc: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	inputs := []string{"\xff\xfeA", "\xc3(", "plain"}
	want := []string{"\uFFFDA", "\uFFFD(", "plain"}
	for i, in := range inputs {
		if _, err := raw.Exec(`INSERT INTO t (id, body) VALUES (?, ?)`, i, in); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	repairs, err := repairInvalidText(conn)
	if err != nil {
		t.Fatalf("repairInvalidText: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn close: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close modernc: %v", err)
	}

	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw (Turso): %v", err)
	}
	defer func() { _ = pool.Close() }()

	rows, err := pool.Query(`SELECT body FROM t ORDER BY id`)
	if err != nil {
		t.Fatalf("query under Turso: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read under Turso: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows under Turso = %q, want %q", got, want)
	}
	if n := totalRepaired(repairs); n != 2 {
		t.Errorf("repaired %d values, want 2 (%v)", n, repairs)
	}
}

// totalRepaired sums the per-column counts a repair returned.
func totalRepaired(repairs []ColumnRepair) int {
	n := 0
	for _, r := range repairs {
		n += r.Repaired
	}
	return n
}
