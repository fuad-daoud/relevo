package syncworker

import (
	"context"
	"database/sql"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	turso "turso.tech/database/tursogo"
)

// TestMigrateReportsThisCallsBytes pins that the result carries this call's
// deltas, not the backend's lifetime totals.
func TestMigrateReportsThisCallsBytes(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
	store := blobstore.NewMemBlobStore(nil)
	b, driver := openMigrateBackend(t, path, store, "origin-a")

	b.bytes.TursoSent, b.bytes.TursoReceived = 1000, 2000
	b.bytes.R2Put, b.bytes.R2Get = 999, 888
	driver.engineBytes = turso.TursoSyncDbStats{NetworkSentBytes: 10000, NetworkReceivedBytes: 20000}
	driver.afterPull = func(db *sql.DB) {
		driver.engineBytes.NetworkSentBytes += 7
		driver.engineBytes.NetworkReceivedBytes += 11
	}
	driver.afterPush = func(db *sql.DB) {
		driver.engineBytes.NetworkSentBytes += 13
		driver.engineBytes.NetworkReceivedBytes += 17
	}
	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Two pulls and two pushes: the initial pull, the sweep pull, the flip
	// and the commit.
	wantSent := int64(2*7 + 2*13)
	wantRecv := int64(2*11 + 2*17)
	if result.TursoSent != wantSent || result.TursoReceived != wantRecv {
		t.Errorf("turso = %d/%d, want this call's %d/%d", result.TursoSent, result.TursoReceived, wantSent, wantRecv)
	}
	if b.bytes.TursoSent != 1000+wantSent || b.bytes.TursoReceived != 2000+wantRecv {
		t.Errorf("lifetime turso = %d/%d, want %d/%d", b.bytes.TursoSent, b.bytes.TursoReceived, 1000+wantSent, 2000+wantRecv)
	}
	objs, err := store.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list the store: %v", err)
	}
	var wantR2 int64
	for _, o := range objs {
		wantR2 += o.Size
	}
	if result.R2Put != wantR2 {
		t.Errorf("R2Put = %d, want the new objects' %d, not the lifetime 999 above it", result.R2Put, wantR2)
	}
	if b.bytes.R2Put != 999+wantR2 {
		t.Errorf("lifetime R2Put = %d, want 999 + %d", b.bytes.R2Put, wantR2)
	}
	if result.R2Get != 0 {
		t.Errorf("R2Get = %d, want 0 on a clean run with no re-verification", result.R2Get)
	}
	if b.bytes.R2Get != 888 {
		t.Errorf("lifetime R2Get = %d, want the 888 it held: no fetch ran", b.bytes.R2Get)
	}
}

// TestMigrateReadsAnUnparsableFloorAsZero pins that a floor row that is not a
// number migrates as 0 rather than refusing the migration over one row.
func TestMigrateReadsAnUnparsableFloorAsZero(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 5))
	execOn(t, path, "INSERT INTO meta (key, value) VALUES ('floor.origin-b', 'garbage')")
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("migrate with a garbage floor: %v", err)
	}
	if got := result.Origins["origin-b"].Floor; got != 0 {
		t.Errorf("floor = %d, want 0 for an unparsable row", got)
	}
	if h := readHeadsRow(t, path, "origin-b"); h.floor != 0 {
		t.Errorf("heads floor = %d, want 0 for an unparsable row", h.floor)
	}
}

// TestLabelOf pins the label read: a missing label, a body that is not an
// object, and a label that is not a string each read as empty, and only a
// string label is kept. Mutants: take the label from the oldest upsert, write
// floor as 0.
func TestLabelOf(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"a label is kept", `{"label":"laptop"}`, "laptop"},
		{"a missing label reads empty", `{"title":"no label"}`, ""},
		{"a body that is not an object reads empty", `[1,2]`, ""},
		{"a label that is not a string reads empty", `{"label":7}`, ""},
		{"not JSON reads empty", `not json`, ""},
	}
	for _, tc := range cases {
		if got := labelOf(tc.body); got != tc.want {
			t.Errorf("%s: labelOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMigrateTakesTheNewestLabel pins that the newest installation upsert
// wins: two upserts for one origin migrate with the later label, and an
// origin with no installation row migrates with an empty label.
func TestMigrateTakesTheNewestLabel(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, []Entry{
		installEntry("origin-b", "first", 1, 1),
		installEntry("origin-b", "second", 2, 2),
	})
	store := blobstore.NewMemBlobStore(nil)
	b, _ := openMigrateBackend(t, path, store, "origin-a")
	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got := result.Origins["origin-b"].Label; got != "second" {
		t.Errorf("label = %q, want the newest upsert's second", got)
	}
	if h := readHeadsRow(t, path, "origin-b"); h.label != "second" {
		t.Errorf("heads label = %q, want the newest upsert's second", h.label)
	}
}
