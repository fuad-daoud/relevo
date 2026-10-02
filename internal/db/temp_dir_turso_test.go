//go:build !modernc

package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareTempDir(t *testing.T) {
	t.Setenv("TURSO_TMPDIR", "")
	t.Setenv("SQLITE_TMPDIR", "")
	dir := t.TempDir()
	wantTmp := filepath.Join(dir, "tmp")

	if err := prepareTempDir(dir); err != nil {
		t.Fatalf("prepareTempDir: %v", err)
	}

	info, err := os.Stat(wantTmp)
	if err != nil {
		t.Fatalf("stat tmp dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", wantTmp)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("tmp dir mode = %o, want 0700", mode)
	}

	if got := os.Getenv("TURSO_TMPDIR"); got != wantTmp {
		t.Errorf("TURSO_TMPDIR = %q, want %q", got, wantTmp)
	}
	if got := os.Getenv("SQLITE_TMPDIR"); got != wantTmp {
		t.Errorf("SQLITE_TMPDIR = %q, want %q", got, wantTmp)
	}
}

func TestPrepareTempDirPreservesExistingEnv(t *testing.T) {
	customTurso := "/custom/turso/tmp"
	customSQLite := "/custom/sqlite/tmp"
	t.Setenv("TURSO_TMPDIR", customTurso)
	t.Setenv("SQLITE_TMPDIR", customSQLite)

	dir := t.TempDir()
	if err := prepareTempDir(dir); err != nil {
		t.Fatalf("prepareTempDir: %v", err)
	}

	if got := os.Getenv("TURSO_TMPDIR"); got != customTurso {
		t.Errorf("TURSO_TMPDIR = %q, want %q", got, customTurso)
	}
	if got := os.Getenv("SQLITE_TMPDIR"); got != customSQLite {
		t.Errorf("SQLITE_TMPDIR = %q, want %q", got, customSQLite)
	}
}

func TestConnectionReportsTempStoreMemory(t *testing.T) {
	dir := t.TempDir()
	wantTmp := filepath.Join(dir, "tmp")
	t.Setenv("TURSO_TMPDIR", "")
	t.Setenv("SQLITE_TMPDIR", "")
	if err := prepareTempDir(dir); err != nil {
		t.Fatalf("prepareTempDir: %v", err)
	}

	dbPath := filepath.Join(dir, "test.db")
	pool, err := openPool(dbPath, 5*time.Second, false)
	if err != nil {
		t.Fatalf("openPool: %v", err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close pool: %v", err)
		}
	}()

	var tempStore int
	if err := pool.QueryRow("PRAGMA temp_store").Scan(&tempStore); err != nil {
		t.Fatalf("PRAGMA temp_store: %v", err)
	}
	// 2 is MEMORY (0 = DEFAULT, 1 = FILE, 2 = MEMORY).
	if tempStore != 2 {
		t.Errorf("PRAGMA temp_store = %d, want 2 (MEMORY)", tempStore)
	}

	if got := os.Getenv("TURSO_TMPDIR"); got != wantTmp {
		t.Errorf("TURSO_TMPDIR = %q, want %q", got, wantTmp)
	}
}
