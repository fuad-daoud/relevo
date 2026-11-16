package syncworker

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// insertLateLogRow inserts one format-5 log row above the migrated segments.
func insertLateLogRow(t *testing.T, db *sql.DB, origin string, seq int) {
	t.Helper()
	if _, err := db.Exec(
		"INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		origin, seq, seq, "board", `["late"]`, opUpsert, 3, `{"title":"late"}`,
		time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC).Format(time.RFC3339Nano),
		nil, nil, nil); err != nil {
		t.Fatalf("insert the late row: %v", err)
	}
}

// TestMigrateSweepsALateRow pins that a row an old binary wrote after the
// read is swept: a new object at that seq, RowsSwept 1, and heads at the row.
func TestMigrateSweepsALateRow(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
	store := blobstore.NewMemBlobStore(nil)
	b, driver := openMigrateBackend(t, path, store, "origin-a")

	pulls := 0
	driver.afterPull = func(db *sql.DB) {
		pulls++
		if pulls == 2 {
			insertLateLogRow(t, db, "origin-b", 11)
		}
	}
	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("migrate with one late row: %v", err)
	}
	if result.RowsSwept != 1 {
		t.Errorf("rows swept = %d, want 1 for the late row", result.RowsSwept)
	}
	if h := readHeadsRow(t, path, "origin-b"); h.seq != 11 {
		t.Errorf("heads origin-b seq = %d, want the late row's 11", h.seq)
	}
	key := blobstore.SegmentKey("origin-b", 11)
	if _, err := store.Get(context.Background(), key, &discardWriter{}); err != nil {
		t.Errorf("no new object at the late seq %s: %v", key, err)
	}
	_ = pulls
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestMigrateRefusesWhenLateRowsKeepComing pins the bound: a hook inserting
// on every pull refuses with CodeMigrateIncomplete, the old tables stay,
// format stays migrating and no heads exist; a second run with the hook off
// sweeps and completes. Calling commitMigration directly with cover below the
// log max refuses the same way and rolls back.
func TestMigrateRefusesWhenLateRowsKeepComing(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
	store := blobstore.NewMemBlobStore(nil)
	b, driver := openMigrateBackend(t, path, store, "origin-a")

	next := 11
	driver.afterPull = func(db *sql.DB) {
		insertLateLogRow(t, db, "origin-b", next)
		next++
	}
	_, err := b.Migrate()
	if err == nil {
		t.Fatal("a run with rows on every pull reported no error")
	}
	if code := RefusalCodeOf(err); code != CodeMigrateIncomplete {
		t.Errorf("code = %q, want %q", code, CodeMigrateIncomplete)
	}
	if !strings.Contains(err.Error(), "origin-b") {
		t.Errorf("refusal %q does not name the blocking origin", err)
	}
	tables := replicaTables(t, path)
	for _, want := range []string{"log", "marks", "meta"} {
		if !tables[want] {
			t.Errorf("after the refusal the replica lacks %q: %v", want, tables)
		}
	}
	if tables["heads"] {
		t.Error("after the refusal the replica holds heads, want none")
	}
	if got := metaFormat(t, path); got != migratingFormat {
		t.Errorf("meta format = %q, want %q after the refusal", got, migratingFormat)
	}
	beforeTables := replicaTables(t, path)
	beforeRows := snapshotLog(t, path)

	driver.afterPull = nil
	_, err = b.Migrate()
	if err != nil {
		t.Fatalf("the second run with the hook off: %v", err)
	}
	if h := readHeadsRow(t, path, "origin-b"); h.seq < 11 {
		t.Errorf("heads origin-b seq = %d, want the late rows swept", h.seq)
	}

	// A direct commit with cover below the log max refuses and rolls back.
	directPath := tempReplica(t)
	seedFormatFiveRemote(t, directPath)
	seedFormatFiveLog(t, directPath, legacyLogEntries("origin-b", 1, 10))
	directStore := blobstore.NewMemBlobStore(nil)
	direct, _ := openMigrateBackend(t, directPath, directStore, "origin-a")
	ctx := context.Background()
	maxes, err := direct.logOriginMaxes(ctx)
	if err != nil {
		t.Fatalf("read the maxes: %v", err)
	}
	_ = maxes
	covered := map[string]int{"origin-b": 5}
	writes := map[string]originWrite{"origin-b": {rows: 5, last: 5, lastFirst: 1}}
	beforeDirectTables := replicaTables(t, directPath)
	beforeDirectRows := snapshotLog(t, directPath)
	if _, _, _, err := direct.commitMigration(ctx, covered, writes); err == nil {
		t.Fatal("commit with cover below the max reported no error")
	} else if code := RefusalCodeOf(err); code != CodeMigrateIncomplete {
		t.Errorf("direct commit code = %q, want %q", code, CodeMigrateIncomplete)
	}
	if after := replicaTables(t, directPath); !reflect.DeepEqual(after, beforeDirectTables) {
		t.Errorf("tables after the refused commit = %v, want %v: the commit rolled back", after, beforeDirectTables)
	}
	if after := snapshotLog(t, directPath); !reflect.DeepEqual(after, beforeDirectRows) {
		t.Error("log rows after the refused commit changed, want a rollback")
	}
	_ = beforeTables
	_ = beforeRows
}
