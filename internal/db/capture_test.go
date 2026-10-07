//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// captureChanges is how many rows of one table the change set holds, counted the
// way the push counts them: the rows the driver itself recorded, not the rows
// the table happens to hold.
func captureChanges(t *testing.T, pool *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(`SELECT COUNT(*) FROM turso_cdc WHERE table_name = ?`, table).Scan(&n); err != nil {
		t.Fatalf("count the captured changes for %s: %v", table, err)
	}
	return n
}

// TestTheCaptureConnectionRecordsWhatThePoolDoesNot is the backfill's premise,
// and it is the finding the re-land rests on: a row written through the pool is
// in the database and in no change set, while the same row rewritten through the
// dedicated capture connection is recorded. One database, two connections, two
// different answers.
func TestTheCaptureConnectionRecordsWhatThePoolDoesNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE ticks (id INTEGER PRIMARY KEY, at INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pool.Exec(`INSERT INTO ticks (id, at) VALUES (1, 10)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open the capture connection: %v", err)
	}
	defer func() { _ = capture.Close() }()

	if got := captureChanges(t, pool, "ticks"); got != 0 {
		t.Fatalf("the seeded row is already in the change set (%d changes), so the gap this closes is not there", got)
	}

	res, err := capture.Backfill(context.Background(), "ticks", -1, 16)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Rows != 1 {
		t.Errorf("the backfill recorded %d rows, want 1", res.Rows)
	}
	if got := captureChanges(t, pool, "ticks"); got == 0 {
		t.Error("the backfill left the change set empty, so the row the seed wrote is still in no change set")
	}

	// The row is still the row: a backfill that rewrote it into something else
	// would have carried the wrong bytes to the remote.
	var at int
	if err := pool.QueryRow(`SELECT at FROM ticks WHERE id = 1`).Scan(&at); err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if at != 10 {
		t.Errorf("at = %d after the backfill, want 10", at)
	}
}

// TestTheBackfillWalksATableInBoundedBatches pins the batch and offset contract:
// each batch takes at most limit rows, each resumes after the last rowid the
// previous one reached, and the walk finishes with every row recorded exactly
// once.
func TestTheBackfillWalksATableInBoundedBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batched.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE ticks (id INTEGER PRIMARY KEY, at INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := range 7 {
		if _, err := pool.Exec(`INSERT INTO ticks (id, at) VALUES (?, ?)`, i, i*10); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open the capture connection: %v", err)
	}
	defer func() { _ = capture.Close() }()

	var (
		at      int64 = -1
		batches int
		total   int64
	)
	for {
		res, err := capture.Backfill(context.Background(), "ticks", at, 3)
		if err != nil {
			t.Fatalf("backfill batch %d: %v", batches, err)
		}
		batches++
		total += res.Rows
		if res.Rows < 3 {
			break
		}
		if res.NextRowID <= at {
			t.Fatalf("batch %d reported next rowid %d, want it past %d", batches, res.NextRowID, at)
		}
		at = res.NextRowID
		if batches > 10 {
			t.Fatal("the walk did not finish")
		}
	}
	if total != 7 {
		t.Errorf("the walk recorded %d rows, want 7", total)
	}
	if batches != 3 {
		t.Errorf("the walk took %d batches of 3 over 7 rows, want 3", batches)
	}
}

// TestTheBackfillResumesFromTheMarker pins resumability: a second pass over a
// table the first pass finished records nothing, which is what makes the offset
// a safe place to store. A batch that re-recorded a finished range would
// double every row on the remote.
func TestTheBackfillResumesFromTheMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE ticks (id INTEGER PRIMARY KEY, at INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := range 4 {
		if _, err := pool.Exec(`INSERT INTO ticks (id, at) VALUES (?, ?)`, i, i); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open the capture connection: %v", err)
	}
	defer func() { _ = capture.Close() }()

	first, err := capture.Backfill(context.Background(), "ticks", -1, 16)
	if err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	afterFirst := captureChanges(t, pool, "ticks")

	second, err := capture.Backfill(context.Background(), "ticks", first.NextRowID, 16)
	if err != nil {
		t.Fatalf("resumed backfill: %v", err)
	}
	if second.Rows != 0 {
		t.Errorf("the resumed batch recorded %d rows, want 0", second.Rows)
	}
	if got := captureChanges(t, pool, "ticks"); got != afterFirst {
		t.Errorf("the change set grew from %d to %d on a resume past the last row", afterFirst, got)
	}
}

// TestCaptureTablesNamesTheApplicationsTables pins the walk's table list: the
// file's own tables, and none of the driver's own.
func TestCaptureTablesNamesTheApplicationsTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tables.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE round (id TEXT PRIMARY KEY, body TEXT)`,
		`CREATE TABLE binding (id TEXT PRIMARY KEY)`,
	} {
		if _, err := pool.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open the capture connection: %v", err)
	}
	defer func() { _ = capture.Close() }()

	tables, err := CaptureTables(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("read the tables: %v", err)
	}
	want := map[string]bool{"round": false, "binding": false}
	for _, name := range tables {
		if _, ok := want[name]; ok {
			want[name] = true
		}
		if name == "turso_cdc" || name == "turso_cdc_version" {
			t.Errorf("the table list names the driver's own %s", name)
		}
		if len(name) >= 7 && name[:7] == "sqlite_" {
			t.Errorf("the table list names the engine's own %s", name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("the table list is missing %s: %v", name, tables)
		}
	}
}

// TestTheCaptureConnectionIsNotThePoolPath is the guard against the reverted
// shape: opening a capture connection must not have put the pragma anywhere a
// pooled connection would inherit it. A read through the pool after the capture
// connection opened is the question -- if the pragma had leaked onto the open
// path, this write would be captured too.
func TestTheCaptureConnectionIsNotThePoolPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leak.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE ticks (id INTEGER PRIMARY KEY, at INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open the capture connection: %v", err)
	}
	defer func() { _ = capture.Close() }()

	if _, err := pool.Exec(`INSERT INTO ticks (at) VALUES (1)`); err != nil {
		t.Fatalf("insert through the pool: %v", err)
	}
	if got := captureChanges(t, pool, "ticks"); got != 0 {
		t.Errorf("a write through the pool was captured (%d changes), so the pragma reached the open path", got)
	}
}
