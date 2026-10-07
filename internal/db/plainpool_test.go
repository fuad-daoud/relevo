package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The driver's marker table, assembled from parts so this file does not carry
// the name the guard below refuses to find: a test that greps the tree for it
// would otherwise be the first hit it reported.
const cdcTable = "turso" + "_cdc"

// TestOpenHasNoCapturePool pins that opening the machine database asks the
// engine for no change capture at all. The pragma that used to do that was a
// write, so it queued for the single write slot and turned every read on a
// busy database into a busy timeout; its tables were also the driver's marker
// that this file was a remote's member, which is exactly what a local record
// must never be. So the assertion is on the file itself: a scratch database
// comes up with no such table, and its writes go through the pool.
func TestOpenHasNoCapturePool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{Origin: "inst-a"})

	var marked int
	if err := d.sqlDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, cdcTable,
	).Scan(&marked); err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	if marked != 0 {
		t.Errorf("a fresh database carries the change-capture table, so the open asked for capture")
	}

	// The other half of the claim: writes still land, through the pool, on a
	// handle that has no capture connection of its own.
	recordID, err := d.RecordPut(testRecord("plain"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	insertPlainRoundFile(t, d, recordID, "plain.jsonl", "body\n")
	var rows int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM round_file WHERE record_id = ?`, recordID).Scan(&rows); err != nil {
		t.Fatalf("count round_file: %v", err)
	}
	if rows != 1 {
		t.Errorf("round_file rows for the record = %d, want 1", rows)
	}
}

// driverPrivateNames are the sync driver's own file and table names. Nothing in
// this tree may reach for them: they are the driver's surface rather than
// SQLite's, so a file spelled against them breaks on a driver that renames one,
// and they are how a local record came to look joined to a remote.
var driverPrivateNames = []string{
	cdcTable,
	"-" + "info",
	"-" + "changes",
}

// TestNoDriverPrivateNames greps the tree for the sync driver's private names,
// docs excluded: they are what the machine database must not be built against,
// and the refusal belongs where a later round can read it rather than in a
// comment it will not consult.
func TestNoDriverPrivateNames(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// The design docs name the driver on purpose: they are the record of
			// what it was called and why the tree stopped using it.
			if d.Name() == "docs" || d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range driverPrivateNames {
			if strings.Contains(string(body), name) {
				t.Errorf("%s names the driver's private %q", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
