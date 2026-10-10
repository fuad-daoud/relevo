//go:build unix

package db

import (
	"os"
	"strings"
	"testing"
	"time"
)

func hasCdcTable(t *testing.T, path string) bool {
	t.Helper()
	var n int
	row := rawSQLDB(t, path).QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, cdcTable)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("look for the capture table in %s: %v", path, err)
	}
	return n > 0
}

func withCdcTable(t *testing.T) *DB {
	t.Helper()
	d := directOpenTestDB(t)
	if _, err := d.sqlDB.Exec(`CREATE TABLE ` + cdcTable + ` (id INTEGER PRIMARY KEY, change BLOB)`); err != nil {
		t.Fatalf("create the capture table: %v", err)
	}
	return d
}

func TestDropTursoCdcOnceBacksUpThenDrops(t *testing.T) {
	d := withCdcTable(t)
	backups := t.TempDir()

	path, ran, err := DropTursoCdcOnce(d, backups, time.Now())
	if err != nil || !ran {
		t.Fatalf("DropTursoCdcOnce = %q, %t, %v; want it to run", path, ran, err)
	}
	if !strings.HasPrefix(path, backups) {
		t.Fatalf("backup path %q is not under %s", path, backups)
	}
	// The order is observable: a backup taken after the drop would not hold the
	// table, and a drop that never ran would leave it in the live file.
	if !hasCdcTable(t, path) {
		t.Error("the backup lacks the capture table: the drop ran before the backup")
	}
	if hasCdcTable(t, d.Path()) {
		t.Error("the live file still holds the capture table")
	}
	if _, ok, _ := d.KVGet(tursoCdcKVKey); !ok {
		t.Error("the done marker was not written")
	}
	if _, ran, err := DropTursoCdcOnce(d, backups, time.Now()); err != nil || ran {
		t.Errorf("second run = %t, %v; want a no-op", ran, err)
	}
}

func TestDropTursoCdcOnceIsNoopWithoutTable(t *testing.T) {
	d := directOpenTestDB(t)
	backups := t.TempDir()

	path, ran, err := DropTursoCdcOnce(d, backups, time.Now())
	if err != nil || !ran {
		t.Fatalf("DropTursoCdcOnce = %q, %t, %v; want a recorded no-op", path, ran, err)
	}
	if path != "" {
		t.Errorf("backup path = %q, want none", path)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 0 {
		t.Errorf("a file without the table was backed up: %d files", len(entries))
	}
	if _, ok, _ := d.KVGet(tursoCdcKVKey); !ok {
		t.Error("the done marker was not written")
	}
}

func TestDropTursoCdcOnceRefusesWithoutSpace(t *testing.T) {
	d := withCdcTable(t)
	backups := t.TempDir()
	orig := freeBytes
	freeBytes = func(string) (uint64, error) { return 0, nil }
	t.Cleanup(func() { freeBytes = orig })

	_, ran, err := DropTursoCdcOnce(d, backups, time.Now())
	if err == nil || ran {
		t.Fatalf("DropTursoCdcOnce = %t, %v; want a refusal", ran, err)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 0 {
		t.Errorf("a backup was taken despite the refusal: %d files", len(entries))
	}
	if !hasCdcTable(t, d.Path()) {
		t.Error("the table was dropped despite the refusal")
	}
	if _, ok, _ := d.KVGet(tursoCdcKVKey); ok {
		t.Error("the marker was written for a refused run")
	}
}

// Room for the backup alone is not enough: the VACUUM after the drop writes a
// second copy to the same disk.
func TestDropTursoCdcOnceWantsRoomForTheVacuumToo(t *testing.T) {
	d := withCdcTable(t)
	info, err := os.Stat(d.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	orig := freeBytes
	freeBytes = func(string) (uint64, error) { return uint64(info.Size()) * 3 / 2, nil } //nolint:gosec // a test file's size
	t.Cleanup(func() { freeBytes = orig })

	if _, ran, err := DropTursoCdcOnce(d, t.TempDir(), time.Now()); err == nil || ran {
		t.Fatalf("DropTursoCdcOnce with room for one copy = %t, %v; want a refusal", ran, err)
	}
	if !hasCdcTable(t, d.Path()) {
		t.Error("the table was dropped despite the refusal")
	}
}
