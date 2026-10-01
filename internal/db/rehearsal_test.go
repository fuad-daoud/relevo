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
// It runs both arrival paths: a copy carrying SQLite's -wal and -shm, and one a
// clean modernc close left with no sidecars, where only the absent marker can
// trigger the conversion. It is skipped unless RELEVO_TURSO_REHEARSAL names a
// database image to convert, so it runs only when a real pre-Turso file is at
// hand.
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
	logRepairs(t, "wal path", sqliteRepairCounts(t, repairSrc))
	rehearseConversion(t, "wal path", converted, wantCounts, wantVersion)

	// The upgrade path a pre-Turso daemon leaves: it drained and closed the
	// database cleanly before re-execing, so the copy has no sidecars and the
	// absent marker is the only trigger left.
	upgradeRepair := filepath.Join(dir, "upgrade-repair-"+base)
	upgrade := filepath.Join(dir, "upgrade-"+base)
	copyDBImage(t, src, upgradeRepair)
	copyDBImage(t, src, upgrade)
	for _, path := range []string{upgradeRepair, upgrade} {
		sqliteCleanClose(t, path)
	}
	logRepairs(t, "upgrade path", sqliteRepairCounts(t, upgradeRepair))
	rehearseConversion(t, "upgrade path", upgrade, wantCounts, wantVersion)
}

// rehearseConversion opens converted under Turso and checks the conversion's
// promise against the modernc baseline.
func rehearseConversion(t *testing.T, label, converted string, wantCounts map[string]int, wantVersion int) {
	t.Helper()
	d, err := Open(converted)
	if err != nil {
		t.Fatalf("%s: db.Open(%s): %v", label, converted, err)
	}
	defer func() { _ = d.Close() }()

	gotCounts := countsFrom(t, d.sqlDB)
	if len(gotCounts) != len(wantCounts) {
		t.Errorf("%s: Turso sees %d tables, modernc saw %d", label, len(gotCounts), len(wantCounts))
	}
	for name, want := range wantCounts {
		if got := gotCounts[name]; got != want {
			t.Errorf("%s: table %s: Turso has %d rows, modernc had %d", label, name, got, want)
		}
	}
	if have, know := d.SchemaVersions(); have != wantVersion {
		t.Errorf("%s: schema version after conversion = %d, want the %d modernc read (this binary knows %d)", label, have, wantVersion, know)
	}
	var check string
	if err := d.sqlDB.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		t.Fatalf("%s: integrity_check: %v", label, err)
	}
	if check != "ok" {
		t.Errorf("%s: integrity_check = %q, want ok", label, check)
	}
	t.Logf("rehearsal %s: %d tables compared, schema version %d, integrity_check %q", label, len(wantCounts), wantVersion, check)
}

// logRepairs writes one line per column the repair touched, so a rehearsal
// records what the conversion changed.
func logRepairs(t *testing.T, label string, repairs []ColumnRepair) {
	t.Helper()
	for _, r := range repairs {
		t.Logf("rehearsal %s repair: %s.%s: %d values repaired", label, r.Table, r.Column, r.Repaired)
	}
}

// sqliteCleanClose opens path under modernc, drains its -wal and closes it, then
// removes any sidecar left behind: the state the old build leaves when its
// daemon drains and closes the database before re-execing onto the new binary.
func sqliteCleanClose(t *testing.T, path string) {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("modernc open %s: %v", path, err)
	}
	if _, err := pool.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = pool.Close()
		t.Fatalf("modernc checkpoint %s: %v", path, err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("modernc close %s: %v", path, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s%s: %v", path, suffix, err)
		}
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
