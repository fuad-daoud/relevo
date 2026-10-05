package ingest_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
)

// The dedupe pass deletes shared history -- the mirror rows a round_file proves
// duplicate -- and records that it finished in a machine-local marker. The two
// live in different files, so the marker must be written to the local one and
// read back from it, or a second pass runs again over rows it already removed.

// dedupePair opens a split pair in a fresh directory and returns it.
func dedupePair(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestTheDedupeMarkerIsWrittenAndReadLocally pins both halves: the marker lands
// in the local file, and a second pass finds it there and does nothing.
func TestTheDedupeMarkerIsWrittenAndReadLocally(t *testing.T) {
	d := dedupePair(t)
	now := time.Now().UTC()
	backup := t.TempDir()

	stats, ran, err := ingest.DedupeMirrorOnce(d, backup, now)
	if err != nil {
		t.Fatalf("DedupeMirrorOnce: %v", err)
	}
	if !ran {
		t.Fatal("the dedupe pass did not run on a fresh pair")
	}
	if _, ok, err := d.KVGet("mirror-dedupe.v2"); err != nil {
		t.Fatalf("read the marker from the shared file: %v", err)
	} else if ok {
		t.Error("the dedupe marker is readable on the shared file, so the pass wrote it there")
	}
	if _, ok, err := d.Local().KVGet("mirror-dedupe.v2"); err != nil {
		t.Fatalf("read the marker from the local file: %v", err)
	} else if !ok {
		t.Error("the dedupe marker is not in the local file")
	}
	if stats.DoneAt.IsZero() {
		t.Error("the pass recorded no finish time")
	}

	second, ran, err := ingest.DedupeMirrorOnce(d, backup, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second DedupeMirrorOnce: %v", err)
	}
	if ran {
		t.Error("the second dedupe pass ran; its marker was not found")
	}
	if !second.DoneAt.IsZero() {
		t.Errorf("the second pass reported stats %+v; a pass that did not run reports none", second)
	}
}
