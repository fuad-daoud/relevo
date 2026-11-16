package syncworker

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// TestMigrateWithoutHello pins that a verb before the handshake refuses.
func TestMigrateWithoutHello(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.Migrate(); !errors.Is(err, ErrNoHello) {
		t.Errorf("migrate with no replica = %v, want ErrNoHello", err)
	}
}

// TestMigrateResumesFromMigratingShape pins that a run starting on a
// 5-migrating replica with the source tables still present continues.
func TestMigrateResumesFromMigratingShape(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	execOn(t, path, "UPDATE meta SET value = '"+migratingFormat+"' WHERE key = '"+metaFormatKey+"'")
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	if _, err := b.Migrate(); err != nil {
		t.Fatalf("migrate from the migrating shape: %v", err)
	}
	if got := metaFormat(t, path); got != "6" {
		t.Errorf("meta format = %q, want 6 after the resumed run", got)
	}
}

// TestMigrateSeesAnotherMachineFinish pins the second check: a pull that
// brings the finished migration answers already-migrated.
func TestMigrateSeesAnotherMachineFinish(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	store := blobstore.NewMemBlobStore(nil)
	b, driver := openMigrateBackend(t, path, store, "origin-a")
	driver.afterPull = func(db *sql.DB) {
		driver.afterPull = nil
		for _, stmt := range []string{
			createHeadsStatement,
			createSharedStatement,
			"UPDATE meta SET value = '6' WHERE key = '" + metaFormatKey + "'",
			"DROP TABLE log",
			"DROP TABLE marks",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Errorf("the pull brings %q: %v", stmt, err)
			}
		}
	}
	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("migrate after another machine finished: %v", err)
	}
	if !result.AlreadyMigrated {
		t.Error("the run did not report already-migrated after the pull brought format 6")
	}
}

// TestMigrateReportsATransferFailure pins that a pull or push the engine
// refuses is reported rather than migrated around.
func TestMigrateReportsATransferFailure(t *testing.T) {
	t.Run("the first pull failing stops the run", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFiveRemote(t, path)
		seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
		store := blobstore.NewMemBlobStore(nil)
		b, driver := openMigrateBackend(t, path, store, "origin-a")
		driver.pullErr = errors.New("connection reset by peer")
		if _, err := b.Migrate(); err == nil {
			t.Error("migrate past a failed pull reported no error")
		}
	})
	t.Run("the flip push failing stops the run", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFiveRemote(t, path)
		seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
		store := blobstore.NewMemBlobStore(nil)
		b, driver := openMigrateBackend(t, path, store, "origin-a")
		driver.pushErr = errors.New("connection reset by peer")
		if _, err := b.Migrate(); err == nil {
			t.Error("migrate past a failed flip push reported no error")
		}
		if got := metaFormat(t, path); got == "6" {
			t.Error("the flip's failed push still migrated the format")
		}
	})
}

// TestMigrateRefusesAnUnkeyableOrigin pins the up-front refusal: an origin
// that cannot be keyed refuses before any object exists.
func TestMigrateRefusesAnUnkeyableOrigin(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, []Entry{{
		Origin: "bad/origin", Seq: 1, Batch: 1, Tbl: "board", PK: `["k1"]`,
		Op: opUpsert, SchemaVersion: 3, Body: []byte(`{"title":"one"}`),
		At: pinnedAt,
	}})
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	_, err := b.Migrate()
	if err == nil {
		t.Fatal("migrated an origin that cannot be keyed")
	}
	if code := RefusalCodeOf(err); code != CodeRemote {
		t.Errorf("code = %q, want %q", code, CodeRemote)
	}
	if n := totalObjects(t, store); n != 0 {
		t.Errorf("the store holds %d objects, want none: the refusal runs before any put", n)
	}
}

// TestMigrateMigratesAnEmptyLog pins the degenerate remainder: a log with no
// rows still becomes format 6 with empty heads and shared.
func TestMigrateMigratesAnEmptyLog(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("migrate an empty log: %v", err)
	}
	if result.Rows != 0 || result.SharedRows != 0 {
		t.Errorf("result = %+v, want no rows and no shared rows", result)
	}
	if len(result.Origins) != 0 {
		t.Errorf("result origins = %v, want none", result.Origins)
	}
	if got := metaFormat(t, path); got != "6" {
		t.Errorf("meta format = %q, want 6", got)
	}
	tables := replicaTables(t, path)
	if len(tables) != 3 || !tables["heads"] || !tables["meta"] || !tables["shared"] {
		t.Errorf("tables = %v, want exactly heads, meta and shared", tables)
	}
}

// TestCommitMigrationWithoutSegments pins the guard: a commit with cover but
// no writes for an origin reports which origin lacks its segments.
func TestCommitMigrationWithoutSegments(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	ctx := context.Background()
	covered := map[string]int{"origin-b": 5}
	if _, _, _, err := b.commitMigration(ctx, covered, nil); err == nil {
		t.Error("commit without the origin's segments reported no error")
	} else if got := err.Error(); !contains(got, "origin-b") {
		t.Errorf("error %q does not name the origin without its segments", got)
	}
}

// TestMigrateClearsPartialHeads pins that a crashed run's partial tables do
// not stop the next run: heads and shared beside the log are dropped and
// rebuilt.
func TestMigrateClearsPartialHeads(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	execOn(t, path,
		"UPDATE meta SET value = '"+migratingFormat+"' WHERE key = '"+metaFormatKey+"'",
		createHeadsStatement,
		"INSERT INTO heads (origin, seq, key, entries, bytes, floor, legacy_upto, label, at)"+
			" VALUES ('origin-b', 1, 'origin-b/log/000000000001', 1, 10, 0, 1, 'stale', '2026-10-08T09:30:00Z')",
	)
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	if _, err := b.Migrate(); err != nil {
		t.Fatalf("migrate over partial heads: %v", err)
	}
	if h := readHeadsRow(t, path, "origin-b"); h.seq != 5 || h.label != "" {
		t.Errorf("heads = %+v, want the migration's row, not the stale partial", h)
	}
}
