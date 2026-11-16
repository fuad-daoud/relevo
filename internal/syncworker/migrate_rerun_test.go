package syncworker

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// TestMigrateCrashAfterCommitBeforePushReturnsAlreadyMigrated pins the lost
// push.
func TestMigrateCrashAfterCommitBeforePushReturnsAlreadyMigrated(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
	store := blobstore.NewMemBlobStore(nil)
	b, driver := openMigrateBackend(t, path, store, "origin-a")

	driver.afterPull = func(db *sql.DB) {
		if driver.pulls == 2 {
			driver.pushErr = errors.New("connection reset by peer")
		}
	}
	_, err := b.Migrate()
	if err == nil {
		t.Fatal("the run with the commit push failing reported no error")
	}
	if got := metaFormat(t, path); got != "6" {
		t.Errorf("meta format = %q, want 6 locally after the commit", got)
	}
	tables := replicaTables(t, path)
	for _, want := range []string{"heads", "meta", "shared"} {
		if !tables[want] {
			t.Errorf("after the commit the replica lacks %q", want)
		}
	}
	if tables["log"] || tables["marks"] {
		t.Errorf("after the commit the replica still holds the old tables: %v", tables)
	}
	pushesAfterCrash := driver.pushes
	driver.pushErr = nil
	driver.afterPull = nil
	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("the re-run: %v", err)
	}
	if !result.AlreadyMigrated {
		t.Error("the re-run did not report already-migrated")
	}
	if driver.pushes != pushesAfterCrash+1 {
		t.Errorf("pushes = %d, want %d: the re-run finishes the lost push", driver.pushes, pushesAfterCrash+1)
	}
}

// TestMigrateRerunTouchesNoStore pins that a completed migration re-runs
// without touching the store.
func TestMigrateRerunTouchesNoStore(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
	base := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, base, "origin-a")
	if _, err := b.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = b.Close()

	probe := &countStore{BlobStore: base}
	b2 := NewTursoBackend(&localDriver{path: path})
	b2.openStore = func(R2Config) blobstore.BlobStore { return probe }
	if err := b2.Open(Spec{
		Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://scratch.example",
		Token: "the-token", R2: &R2Config{Bucket: "bucket"},
		MirrorTables: migrateMirror, Migrate: true,
	}); err != nil {
		t.Fatalf("re-open the migrated replica: %v", err)
	}
	t.Cleanup(func() { _ = b2.Close() })
	result, err := b2.Migrate()
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if !result.AlreadyMigrated {
		t.Error("the re-run did not report already-migrated")
	}
	if probe.calls != 0 {
		t.Errorf("the re-run made %d store calls, want none", probe.calls)
	}
}
