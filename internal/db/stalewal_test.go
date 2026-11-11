//go:build !modernc

package db

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// openLegacyTestDB opens path with the pre-Turso driver, the way a file this
// build converts was written: rows stay in the -wal with a -shm beside it
// until something checkpoints under the live pool.
func openLegacyTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path}).String() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("modernc open: %v", err)
	}
	return pool
}

// TestPoisonedPoolFailsVersionUntilReopened pins the premise the WAL heal is
// built on: a second handle checkpointing the WAL under a live pool poisons
// every later statement through a pool opened on those files with a short WAL
// read, and closing every handle and reopening the path heals it, because the
// last close drops the stale WAL index and the reopened pool rebuilds its
// own. It fails if the reopened pool is the poisoned handle instead of the
// file: the poison lives in the pool, not the path.
func TestPoisonedPoolFailsVersionUntilReopened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	// The file starts as a legacy one, written by the pre-Turso driver with
	// its rows still in the -wal; it stays open so the -shm outlives the
	// checkpoint below.
	seeder := openLegacyTestDB(t, path)
	if _, err := seeder.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := seeder.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (?, '2026-01-01T00:00:00.000Z')`, embeddedVersion(t)); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if _, err := seeder.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := seeder.Exec(`INSERT INTO t (n) VALUES (?)`, i); err != nil {
			t.Fatalf("seed insert %d: %v", i, err)
		}
	}

	// The live pool opens the legacy file directly, conversion aside, and
	// reads it once: the read is what adopts the WAL index the checkpoint
	// below leaves stale.
	liveRaw, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open live: %v", err)
	}
	defer func() { _ = liveRaw.Close() }()
	live := &DB{sqlDB: liveRaw}
	if _, err := live.Version(); err != nil {
		t.Fatalf("Version before the poison = %v, want success", err)
	}

	// The conversion's own step, run from another handle while the live pool
	// is open: it moves every frame into the main file and truncates the WAL
	// under it.
	poisoner := openLegacyTestDB(t, path)
	if _, err := poisoner.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("poison checkpoint: %v", err)
	}
	if err := poisoner.Close(); err != nil {
		t.Fatalf("poisoner close: %v", err)
	}

	poisonedRaw, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open poisoned: %v", err)
	}
	defer func() { _ = poisonedRaw.Close() }()
	poisoned := &DB{sqlDB: poisonedRaw}
	if _, err := poisoned.Version(); !wire.IsShortReadWAL(err) {
		t.Fatalf("Version through the poisoned pool = %v, want the short WAL read", err)
	}
	if _, err := poisoned.Version(); !wire.IsShortReadWAL(err) {
		t.Fatalf("second Version through the poisoned pool = %v, want the short WAL read", err)
	}

	if err := poisonedRaw.Close(); err != nil {
		t.Fatalf("poisoned close: %v", err)
	}
	if err := liveRaw.Close(); err != nil {
		t.Fatalf("live close: %v", err)
	}
	if err := seeder.Close(); err != nil {
		t.Fatalf("seeder close: %v", err)
	}

	healed, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = healed.Close() }()
	if _, err := healed.Version(); err != nil {
		t.Fatalf("Version after reopen = %v, want success", err)
	}
	var n int
	if err := healed.sqlDB.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count after reopen: %v", err)
	}
	if n != 50 {
		t.Errorf("count after reopen = %d, want the 50 seeded rows back", n)
	}
}
