package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A finished split marker stops the pass, but it does not stop another
// installation sharing this database from writing a machine-local row into it.
// Every reader on this machine is bound to the local file and would never see
// that row again, so a pass whose marker is already present converges instead
// of returning: it moves what is left, and it does not re-stamp itself.

// splitMarker reads the marker the split wrote.
func splitMarker(t *testing.T, d *DB) SplitStats {
	t.Helper()
	raw, ok, err := d.Local().KVGet(splitKVKey)
	if err != nil || !ok {
		t.Fatalf("read the split marker: %v (present %t)", err, ok)
	}
	var stats SplitStats
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatalf("decode the split marker %s: %v", raw, err)
	}
	return stats
}

// findSplitBackup names the one pre-split backup under dir, or "" when there is
// none.
func findSplitBackup(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "relevo.db.pre-split-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// The names the strays use, kept off the first pass's own rows so a stray is
// never confused with one the pass already moved.
const (
	straySecret = "serve.tls.cert"
	strayKey    = "serve.daemon"
)

// seedStrayLocalRow writes a machine-local row and kv key straight into the
// shared file, the way another installation sharing this database would.
func seedStrayLocalRow(t *testing.T, d *DB) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.sqlDB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		straySecret, []byte("token-from-another-installation"), "2026-10-03T10:00:00.000Z")
	exec(`INSERT OR REPLACE INTO kv (key, value_json, updated_at) VALUES (?, ?, ?)`,
		strayKey, `["stray"]`, "2026-10-03T10:00:00.000Z")
}

// TestAConvergePassMovesStraysWithTheMarkerUnchanged pins the whole behaviour:
// the first pass runs and stamps itself, a stray row appears in the shared
// file, and the second pass moves it -- while the marker keeps the first pass's
// own DoneAt and backup name, and no second backup appears.
func TestAConvergePassMovesStraysWithTheMarkerUnchanged(t *testing.T) {
	d, backupDir := openSplitTest(t)
	seedSplitFixture(t, d)

	if _, ran, err := SplitOnce(d, backupDir, splitNow); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	} else if !ran {
		t.Fatal("the first pass did not run on a database holding local rows")
	}
	before := splitMarker(t, d)
	if before.DoneAt != splitNow {
		t.Errorf("the marker records done_at %s, want the pass's own stamp %s", before.DoneAt, splitNow)
	}
	firstBackup := findSplitBackup(t, backupDir)
	if firstBackup == "" {
		t.Fatalf("the first pass moved rows but left no backup under %s", backupDir)
	}

	seedStrayLocalRow(t, d)
	if _, ok, err := d.Local().SecretGet(straySecret); err != nil {
		t.Fatalf("read the local file's secret: %v", err)
	} else if ok {
		t.Fatal("the local file already carries the stray secret; the fixture did not simulate a stray")
	}

	stats, ran, err := SplitOnce(d, backupDir, splitNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("converge SplitOnce: %v", err)
	}
	if !ran {
		t.Fatal("the second pass did nothing; a local-scoped row in the shared file was left behind")
	}
	if stats.RowsMoved == 0 || stats.KVKeysMoved == 0 {
		t.Errorf("the converge pass moved (%d rows, %d keys), want the stray row and key", stats.RowsMoved, stats.KVKeysMoved)
	}

	value, ok, err := d.Local().SecretGet(straySecret)
	if err != nil || !ok {
		t.Fatalf("the local file's secret after the converge = %v, %t, want the stray row", err, ok)
	}
	if string(value) != "token-from-another-installation" {
		t.Errorf("the local secret = %q, want the stray value", value)
	}
	if _, ok, err := d.SecretGet(straySecret); err != nil {
		t.Fatalf("read the shared file's secret: %v", err)
	} else if ok {
		t.Error("the stray secret is still readable on the shared file")
	}
	if _, ok, err := d.KVGet(strayKey); err != nil {
		t.Fatalf("read the shared file's kv: %v", err)
	} else if ok {
		t.Error("the stray kv key is still readable on the shared file")
	}

	after := splitMarker(t, d)
	if after.DoneAt != before.DoneAt {
		t.Errorf("the marker records done_at %s after the converge, want the first pass's %s", after.DoneAt, before.DoneAt)
	}
	if after.BackupPath != before.BackupPath {
		t.Errorf("the marker records backup %q after the converge, want the first pass's %q", after.BackupPath, before.BackupPath)
	}
	if after.RowsMoved != before.RowsMoved || after.KVKeysMoved != before.KVKeysMoved {
		t.Errorf("the marker records (%d rows, %d keys) after the converge, want the first pass's (%d, %d)",
			after.RowsMoved, after.KVKeysMoved, before.RowsMoved, before.KVKeysMoved)
	}
}

// TestAConvergePassReportsNoStampOfItsOwn pins what the converge pass hands its
// caller: the rows it moved under its own time, and no DoneAt, so a log line
// cannot read as a second finished pass.
func TestAConvergePassReportsNoStampOfItsOwn(t *testing.T) {
	d, backupDir := openSplitTest(t)
	seedSplitFixture(t, d)
	if _, _, err := SplitOnce(d, backupDir, splitNow); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}

	seedStrayLocalRow(t, d)
	stats, ran, err := SplitOnce(d, backupDir, splitNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("converge SplitOnce: %v", err)
	}
	if !ran {
		t.Fatal("the converge pass reported no work")
	}
	if !stats.DoneAt.IsZero() {
		t.Errorf("the converge pass reports done_at %s, want none: the marker was not re-stamped", stats.DoneAt)
	}
	if stats.BackupPath == "" {
		t.Error("the converge pass moved rows and reports no backup; it backs up before it moves anything")
	}
}

// TestAPassWithNothingLeftTakesNoBackup pins the quiet case: a converge with
// nothing to move writes no backup and reports no work, so a start on an
// already-split pair is as quiet as it was before.
func TestAPassWithNothingLeftTakesNoBackup(t *testing.T) {
	d, backupDir := openSplitTest(t)
	seedSplitFixture(t, d)
	if _, _, err := SplitOnce(d, backupDir, splitNow); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}

	quiet := t.TempDir()
	stats, ran, err := SplitOnce(d, quiet, splitNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("SplitOnce with nothing to move: %v", err)
	}
	if ran {
		t.Errorf("a pass with nothing to move reported work: %+v", stats)
	}
	if path := findSplitBackup(t, quiet); path != "" {
		t.Errorf("a pass with nothing to move backed the shared file up: %s", path)
	}
}

// TestTheConvergeMarkerIsWrittenOnlyOnce pins that there is exactly one marker
// row however many passes converge: the local file's row count for the key is
// one key, and its value is still the first pass's.
func TestTheConvergeMarkerIsWrittenOnlyOnce(t *testing.T) {
	d, backupDir := openSplitTest(t)
	seedSplitFixture(t, d)
	if _, _, err := SplitOnce(d, backupDir, splitNow); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}
	seedStrayLocalRow(t, d)
	if _, _, err := SplitOnce(d, backupDir, splitNow.Add(time.Hour)); err != nil {
		t.Fatalf("converge SplitOnce: %v", err)
	}

	keys, err := d.Local().KVKeys(splitKVKey)
	if err != nil {
		t.Fatalf("KVKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != splitKVKey {
		t.Errorf("the local file holds %v, want the one marker row %s", keys, splitKVKey)
	}
	if _, ok, err := d.KVGet(splitKVKey); err != nil {
		t.Fatalf("read the shared file's kv: %v", err)
	} else if ok {
		t.Error("the marker is readable on the shared file, so a second marker exists there")
	}
}

// TestAConvergeBacksUpOnlyWhenItMovesSomething pins the backup discipline: the
// converge pass takes its own backup of the shared file before it moves the
// stray row, because the rows it is about to delete are on that file.
func TestAConvergeBacksUpOnlyWhenItMovesSomething(t *testing.T) {
	d, backupDir := openSplitTest(t)
	seedSplitFixture(t, d)
	if _, _, err := SplitOnce(d, backupDir, splitNow); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}

	seedStrayLocalRow(t, d)
	if _, _, err := SplitOnce(d, backupDir, splitNow.Add(time.Hour)); err != nil {
		t.Fatalf("converge SplitOnce: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(backupDir, "relevo.db.pre-split-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 2 {
		t.Errorf("the backup dir holds %d pre-split copies, want one per pass that moved rows: %v", len(matches), matches)
	}
	for _, path := range matches {
		if _, serr := os.Stat(path); serr != nil {
			t.Errorf("backup %s: %v", path, serr)
		}
	}
}
