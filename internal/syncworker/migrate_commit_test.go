package syncworker

import (
	"context"
	"database/sql"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// The migration commit's tests: the label read's failure, and every commit
// failure either through Migrate or, where no hook reaches the statement,
// against the helper directly.

// TestReadTxLabelsReportsAQueryFailure pins that a label query failing after
// the maxes read is reported rather than read as empty: a failed Scan leaves
// the body invalid, which the missing-row check must not mistake for one.
func TestReadTxLabelsReportsAQueryFailure(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	execOn(t, path, "DROP TABLE log")
	ctx := context.Background()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the label read: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = b.readTxLabels(ctx, tx, map[string]int{"origin-b": 5})
	if err == nil {
		t.Fatal("a label query on a dropped log reported no error")
	}
	if !contains(err.Error(), "no such table") || !contains(err.Error(), "read the label") {
		t.Errorf("error %q does not wrap the missing table as a label read", err)
	}
}

// TestMigrateReportsCommitFailures pins every commit statement's failure: a
// hook breaks one statement at the last point before the commit transaction,
// and the rolled-back replica keeps its tables and format.
func TestMigrateReportsCommitFailures(t *testing.T) {
	cases := []struct {
		name string
		hook []string
		want string
	}{
		{"a view over heads", []string{"CREATE VIEW heads AS SELECT 1 AS x"}, "build the migrated tables"},
		{"a dropped meta", []string{"DROP TABLE meta"}, "read the floors"},
		{"a refused floor drop", []string{
			"INSERT INTO meta (key, value) VALUES ('floor.origin-b', '0')",
			"CREATE TRIGGER drop_floor_fail BEFORE DELETE ON meta BEGIN SELECT RAISE(ABORT, 'floor drop refused'); END",
		}, "drop the floors"},
		{"a refused format write", []string{
			"CREATE TRIGGER format_write_fail BEFORE UPDATE ON meta WHEN NEW.value = '6' BEGIN SELECT RAISE(ABORT, 'format write refused'); END",
		}, "write the log format"},
		{"dropped marks", []string{"DROP TABLE marks"}, "drop the old tables"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempReplica(t)
			seedFormatFiveRemote(t, path)
			seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
			store := blobstore.NewMemBlobStore(nil)
			b, driver := openMigrateBackend(t, path, store, "origin-a")
			driver.afterPull = func(db *sql.DB) {
				if driver.pulls == 2 {
					for _, stmt := range tc.hook {
						if _, err := db.Exec(stmt); err != nil {
							t.Fatalf("break the commit with %q: %v", stmt, err)
						}
					}
				}
			}
			_, err := b.Migrate()
			if err == nil {
				t.Fatalf("migrate past %s reported no error", tc.name)
			}
			if !contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
			tables := replicaTables(t, path)
			if !tables["log"] {
				t.Errorf("the refused commit lost the log, want a rollback: %v", tables)
			}
			if tables["heads"] {
				t.Errorf("the refused commit holds heads, want a rollback: %v", tables)
			}
			if tc.name == "a dropped meta" {
				// The hook dropped meta outside the transaction, so no
				// format row is left to read; the log and heads above are
				// what pin the rollback here.
				if tables["meta"] {
					t.Errorf("the refused commit holds meta, want the hook's drop: %v", tables)
				}
				return
			}
			if got := metaFormat(t, path); got != migratingFormat {
				t.Errorf("meta format = %q, want %q after the refused commit", got, migratingFormat)
			}
		})
	}
}

// TestCommitMigrationReportsABeginFailure pins that a commit on a dead handle
// fails at the begin. A direct call, because the verb holds its replica open
// through the commit.
func TestCommitMigrationReportsABeginFailure(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	b, _ := openMigrateBackend(t, path, blobstore.NewMemBlobStore(nil), "origin-a")
	if err := b.db.Close(); err != nil {
		t.Fatalf("close the replica: %v", err)
	}
	if _, _, _, err := b.commitMigration(context.Background(),
		map[string]int{"origin-b": 5},
		map[string]originWrite{"origin-b": {rows: 5, last: 5, lastFirst: 1}}); err == nil {
		t.Error("commit on a closed replica reported no error")
	}
}

// TestCommitMigrationReportsAnUnreadableLog pins that a commit with no log
// fails reading the origins. A direct call, because a hook dropping the log
// would fail the sweep's maxes read before the commit begins.
func TestCommitMigrationReportsAnUnreadableLog(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	b, _ := openMigrateBackend(t, path, blobstore.NewMemBlobStore(nil), "origin-a")
	execOn(t, path, "DROP TABLE log")
	_, _, _, err := b.commitMigration(context.Background(),
		map[string]int{"origin-b": 5},
		map[string]originWrite{"origin-b": {rows: 5, last: 5, lastFirst: 1}})
	if err == nil || !contains(err.Error(), "read the log's origins") {
		t.Errorf("commit with no log = %v, want the origins read to fail", err)
	}
}

// TestInsertTxHelpersReportMissingTables pins that the row writers report the
// tables they write. Direct calls, because commitTx always builds both tables
// before either writer runs.
func TestInsertTxHelpersReportMissingTables(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	b, _ := openMigrateBackend(t, path, blobstore.NewMemBlobStore(nil), "origin-a")
	ctx := context.Background()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the insert: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	writes := map[string]originWrite{"origin-b": {rows: 5, last: 5, lastFirst: 1}}
	if err := insertTxHeads(ctx, tx, "2026-10-08T09:30:00Z",
		map[string]int{"origin-b": 5}, writes, nil, nil); err == nil || !contains(err.Error(), "write the heads") {
		t.Errorf("insertTxHeads with no heads table = %v, want the heads write to fail", err)
	}
	if _, err := b.insertTxShared(ctx, tx); err == nil || !contains(err.Error(), "write the shared rows") {
		t.Errorf("insertTxShared with no shared table = %v, want the shared write to fail", err)
	}
}
