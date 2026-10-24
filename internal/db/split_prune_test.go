package db

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestOriginSchemaIsKeptInTheLocalFile(t *testing.T) {
	d, backupDir := openSplitTest(t)
	if _, _, err := SplitOnce(d, backupDir, splitNow); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}
	if err := d.NoteOriginSchema("zen", 31); err != nil {
		t.Fatalf("NoteOriginSchema: %v", err)
	}
	if _, ok, err := d.KVGet(originSchemaKey); err != nil || ok {
		t.Errorf("shared file holds %s (present %t, err %v), want it only in the local file", originSchemaKey, ok, err)
	}
	if got, err := d.OriginSchemas(); err != nil || got["zen"] != 31 {
		t.Errorf("OriginSchemas = (%v, %v), want zen at 31", got, err)
	}
	stats, ran, err := SplitOnce(d, backupDir, splitNow.Add(time.Hour))
	if err != nil || ran || stats.BackupPath != "" {
		t.Errorf("SplitOnce after NoteOriginSchema = (%+v, ran %t, %v), want nothing to move and no backup", stats, ran, err)
	}
}

func TestPruneSplitBackupsKeepsTheFirstPassAndTheNewest(t *testing.T) {
	d, backupDir := openSplitTest(t)
	seedSplitFixture(t, d)
	first, _, err := SplitOnce(d, backupDir, splitNow)
	if err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}
	var converges []string
	for i := 1; i <= 3; i++ {
		seedStrayLocalRow(t, d)
		stats, _, err := SplitOnce(d, backupDir, splitNow.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatalf("converge SplitOnce %d: %v", i, err)
		}
		converges = append(converges, stats.BackupPath)
	}
	newest := converges[len(converges)-1]
	keptSibling := newest + "-wal"
	droppedSibling := converges[0] + "-shm"
	unrelated := filepath.Join(backupDir, "relevo.db.pre-turso")
	for _, path := range []string{keptSibling, droppedSibling, unrelated} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", path, err)
		}
	}

	if err := PruneSplitBackups(d, backupDir, newest); err != nil {
		t.Fatalf("PruneSplitBackups: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(backupDir, splitBackupPrefix+"*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	want := []string{first.BackupPath, newest, keptSibling}
	slices.Sort(want)
	if !slices.Equal(matches, want) {
		t.Errorf("backups left = %v, want the first pass's and the newest, with its sibling %v", matches, want)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("a file that is not a pre-split backup was touched: %v", err)
	}
}
