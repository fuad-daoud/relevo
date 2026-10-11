//go:build unix

package db

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// reclaimJournal points the default logger at a buffer for the test and hands
// back a reader for what was written, so a test can read the line a skipped
// reclaim actually emitted rather than the one it meant to emit.
func reclaimJournal(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// TestDropTursoCdcSkipsTheReclaimOnAServedHandle pins the change-capture drop
// on the handle a startup pass actually runs on. The table is gone and the
// backup was taken either way; only the file compaction is deferred, and the
// journal says so in one line naming the pass and the reason rather than
// recording a failure nobody can act on.
func TestDropTursoCdcSkipsTheReclaimOnAServedHandle(t *testing.T) {
	d := withCdcTable(t)
	_ = NewOwner(d)
	backups := t.TempDir()

	read := reclaimJournal(t)
	path, ran, err := DropTursoCdcOnce(d, backups, time.Now())
	if err != nil || !ran {
		t.Fatalf("DropTursoCdcOnce = %q, %t, %v; want it to run on a served handle", path, ran, err)
	}
	line := read()

	if hasCdcTable(t, d.Path()) {
		t.Error("the live file still holds the capture table: the drop did not run")
	}
	if !hasCdcTable(t, path) {
		t.Error("the backup lacks the capture table: the drop ran before the backup")
	}
	if !strings.Contains(line, "reclaim skipped") ||
		!strings.Contains(line, "change-capture table drop") ||
		!strings.Contains(line, "the owner serves this handle") {
		t.Errorf("no line names the skipped reclaim and why:\n%s", line)
	}
	if strings.Contains(line, "vacuum_err") {
		t.Errorf("the pass recorded a reclaim failure for a reclaim it skipped:\n%s", line)
	}
}

// TestReclaimVacuumsAnUnservedHandle pins the other side, so the skip is not
// just the answer: an unserved handle compacts exactly as before, and its
// freed pages go back to the filesystem.
func TestReclaimVacuumsAnUnservedHandle(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.sqlDB.Exec(`CREATE TABLE filler (id INTEGER PRIMARY KEY, body BLOB)`); err != nil {
		t.Fatalf("create the filler table: %v", err)
	}
	for i := range 64 {
		body := make([]byte, 4096)
		for j := range body {
			body[j] = byte(i + j)
		}
		if _, err := d.sqlDB.Exec(`INSERT INTO filler (body) VALUES (?)`, body); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	if _, err := d.sqlDB.Exec(`DELETE FROM filler`); err != nil {
		t.Fatalf("empty the filler table: %v", err)
	}
	// The freed pages are in the -wal until it is checkpointed, and the size
	// that matters is the live file's.
	if err := d.walCheckpoint(); err != nil {
		t.Fatalf("checkpoint before measuring: %v", err)
	}
	before := fileSize(t, d.Path())

	if err := d.Reclaim("test pass"); err != nil {
		t.Fatalf("Reclaim on an unserved handle: %v", err)
	}
	if after := fileSize(t, d.Path()); after >= before {
		t.Errorf("the file is %d bytes after a reclaim that ran, want fewer than the %d it had: the compaction did not happen", after, before)
	}
}

// TestReclaimRecordsNothingOnAServedHandle pins the other half of the skip: it
// is not a refusal dressed up, and the pool the owner is serving is left alone.
func TestReclaimRecordsNothingOnAServedHandle(t *testing.T) {
	d := openTestDB(t)
	_ = NewOwner(d)
	read := reclaimJournal(t)

	if err := d.Reclaim("test pass"); err != nil {
		t.Fatalf("Reclaim on a served handle = %v, want a skip rather than a failure", err)
	}
	line := read()
	if !strings.Contains(line, "reclaim skipped") || !strings.Contains(line, "test pass") {
		t.Errorf("no line names the skipped reclaim and its pass:\n%s", line)
	}
	// The handle still answers, which is the whole point: a refusal here would
	// have been a pass that stopped short of its own work.
	if _, _, err := d.KVGet("reclaim-pin"); err != nil {
		t.Errorf("the served handle stopped answering after a skipped reclaim: %v", err)
	}
}

// fileSize is the size of the live database file, which is what a reclaim moves.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}
