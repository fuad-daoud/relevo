package syncworker

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// TestMigrateCrashAfterFirstSegmentResumes pins the create-only re-run.
func TestMigrateCrashAfterFirstSegmentResumes(t *testing.T) {
	seed := func(t *testing.T, path string) {
		t.Helper()
		seedFormatFiveRemote(t, path)
		seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 4106))
	}
	path := tempReplica(t)
	seed(t, path)
	base := blobstore.NewMemBlobStore(nil)
	probe := &failPutStore{BlobStore: base, failAt: 2, err: errors.New("bucket unreachable")}
	b, driver := openMigrateBackend(t, path, probe, "origin-a")

	var pushedFormat string
	driver.afterPush = func(db *sql.DB) {
		var v string
		if err := db.QueryRow("SELECT value FROM meta WHERE key = ?", metaFormatKey).Scan(&v); err == nil {
			pushedFormat = v
		}
	}

	if _, err := b.Migrate(); err == nil {
		t.Fatal("the crashed run reported no error")
	}
	if got := metaFormat(t, path); got != migratingFormat {
		t.Errorf("meta format = %q, want %q after the crash", got, migratingFormat)
	}
	tables := replicaTables(t, path)
	for _, want := range []string{"log", "marks", "meta"} {
		if !tables[want] {
			t.Errorf("after the crash the replica lacks %q: %v", want, tables)
		}
	}
	if tables["heads"] || tables["shared"] {
		t.Errorf("after the crash the replica holds heads/shared: %v", tables)
	}
	if pushedFormat != migratingFormat {
		t.Errorf("the first push carried format %q, want %q: the flip comes first", pushedFormat, migratingFormat)
	}
	_ = b.Close()

	b2, _ := openMigrateBackend(t, path, base, "origin-a")
	if _, err := b2.Migrate(); err != nil {
		t.Fatalf("the clean re-run: %v", err)
	}
	_ = b2.Close()

	cleanPath := tempReplica(t)
	seed(t, cleanPath)
	cleanStore := blobstore.NewMemBlobStore(nil)
	cleanBackend, _ := openMigrateBackend(t, cleanPath, cleanStore, "origin-a")
	if _, err := cleanBackend.Migrate(); err != nil {
		t.Fatalf("the clean run: %v", err)
	}
	_ = cleanBackend.Close()

	if !reflect.DeepEqual(storeBytes(t, base), storeBytes(t, cleanStore)) {
		t.Error("the resumed run's objects differ from the clean run's: compare key -> bytes maps")
	}
}
