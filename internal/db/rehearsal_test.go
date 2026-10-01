//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestTursoConversionRehearsal runs the one-time conversion against a copy of a
// real database and checks its promise: every table keeps its row count, the
// schema version is unchanged, and the file passes integrity_check under Turso.
// It is skipped unless RELEVO_TURSO_REHEARSAL names a database image to convert,
// so it runs only when a real pre-Turso file is at hand.
func TestTursoConversionRehearsal(t *testing.T) {
	src := os.Getenv("RELEVO_TURSO_REHEARSAL")
	if src == "" {
		t.Skip("RELEVO_TURSO_REHEARSAL is not set")
	}

	dir := t.TempDir()
	// Several copies of the same image: one read under modernc for the
	// baseline, one the repair counts run on, and one Turso converts, so
	// neither modernc pass can checkpoint the file before Turso sees it.
	base := filepath.Base(src)
	baseline := filepath.Join(dir, "baseline-"+base)
	repairSrc := filepath.Join(dir, "repair-"+base)
	converted := filepath.Join(dir, "converted-"+base)
	copyDBImage(t, src, baseline)
	copyDBImage(t, src, repairSrc)
	copyDBImage(t, src, converted)

	wantCounts, wantVersion := sqliteTableCounts(t, baseline)
	repairs := sqliteRepairCounts(t, repairSrc)

	d, err := Open(converted)
	if err != nil {
		t.Fatalf("db.Open(%s): %v", converted, err)
	}
	defer func() { _ = d.Close() }()

	gotCounts := countsFrom(t, d.sqlDB)
	if len(gotCounts) != len(wantCounts) {
		t.Errorf("Turso sees %d tables, modernc saw %d", len(gotCounts), len(wantCounts))
	}
	for name, want := range wantCounts {
		if got := gotCounts[name]; got != want {
			t.Errorf("table %s: Turso has %d rows, modernc had %d", name, got, want)
		}
	}
	if have, know := d.SchemaVersions(); have != wantVersion {
		t.Errorf("schema version after conversion = %d, want the %d modernc read (this binary knows %d)", have, wantVersion, know)
	}
	var check string
	if err := d.sqlDB.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if check != "ok" {
		t.Errorf("integrity_check = %q, want ok", check)
	}

	t.Logf("rehearsal: %d tables compared, schema version %d, integrity_check %q", len(wantCounts), wantVersion, check)
	for _, r := range repairs {
		t.Logf("rehearsal repair: %s.%s: %d values repaired", r.Table, r.Column, r.Repaired)
	}
}

// copyDBImage copies src and any -wal/-shm siblings to dst.
func copyDBImage(t *testing.T, src, dst string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(src + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s%s: %v", src, suffix, err)
		}
		if err := os.WriteFile(dst+suffix, data, 0o600); err != nil {
			t.Fatalf("write %s%s: %v", dst, suffix, err)
		}
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// sqliteTableCounts reads each ordinary table's row count and the schema
// version through modernc, without writing the file.
func sqliteTableCounts(t *testing.T, path string) (map[string]int, int) {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_pragma=busy_timeout(5000)"
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("modernc open %s: %v", path, err)
	}
	defer func() { _ = pool.Close() }()
	return countsFrom(t, pool), schemaVersionOf(t, pool)
}

// sqliteRepairCounts runs the conversion's text repair against work under
// modernc and returns the per-column counts.
func sqliteRepairCounts(t *testing.T, work string) []ColumnRepair {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: work}).String() + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("modernc open %s: %v", work, err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("modernc conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	repairs, err := repairInvalidText(conn)
	if err != nil {
		t.Fatalf("repairInvalidText: %v", err)
	}
	return repairs
}

// countsFrom lists the ordinary tables and counts their rows.
func countsFrom(t *testing.T, pool *sql.DB) map[string]int {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	names, err := tableNames(ctx, conn)
	if err != nil {
		t.Fatalf("tableNames: %v", err)
	}
	out := make(map[string]int, len(names))
	for _, name := range names {
		var n int
		if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, quoteIdent(name))).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

// schemaVersionOf reads MAX(version) from schema_version, or zero when the
// table is absent.
func schemaVersionOf(t *testing.T, pool *sql.DB) int {
	t.Helper()
	var v sql.Null[int]
	if err := pool.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		if isMissingTable(err) {
			return 0
		}
		t.Fatalf("schema version: %v", err)
	}
	if !v.Valid {
		return 0
	}
	return v.V
}
