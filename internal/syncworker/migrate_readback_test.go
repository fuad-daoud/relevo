package syncworker

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// TestMigrateReadsEverythingBackEqual pins the whole migration.
func TestMigrateReadsEverythingBackEqual(t *testing.T) {
	path := tempReplica(t)
	seeded := seedMigrateRemote(t, path)
	store := blobstore.NewMemBlobStore(nil)
	b, driver := openMigrateBackend(t, path, store, "origin-a")

	signedBefore, _ := metaRow(t, path, "signed_from.origin-a")
	cutBefore, _ := metaRow(t, path, "epoch_cut.origin-a")

	result, err := b.Migrate()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.AlreadyMigrated {
		t.Fatal("a fresh migration reported already-migrated")
	}
	tables := replicaTables(t, path)
	for _, want := range []string{"heads", "meta", "shared"} {
		if !tables[want] {
			t.Errorf("after the migration the replica lacks %q: %v", want, tables)
		}
	}
	for _, absent := range []string{"log", "marks"} {
		if tables[absent] {
			t.Errorf("after the migration the replica still holds %q", absent)
		}
	}
	if len(tables) != 3 {
		t.Errorf("after the migration the replica holds %v, want exactly heads, meta and shared", tables)
	}
	if got := metaFormat(t, path); got != "6" {
		t.Errorf("meta format = %q, want 6", got)
	}
	byOrigin := make(map[string][]Entry)
	for _, e := range seeded {
		byOrigin[e.Origin] = append(byOrigin[e.Origin], e)
	}
	for origin, rows := range byOrigin {
		sorted := append([]Entry(nil), rows...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
		max := sorted[len(sorted)-1].Seq
		h := readHeadsRow(t, path, origin)
		if h.seq != max {
			t.Errorf("heads %s seq = %d, want the max %d", origin, h.seq, max)
		}
		if h.legacyUpto != max {
			t.Errorf("heads %s legacy_upto = %d, want the max %d", origin, h.legacyUpto, max)
		}
		if h.entries != len(sorted) {
			t.Errorf("heads %s entries = %d, want %d", origin, h.entries, len(sorted))
		}
		sizes, err := store.List(context.Background(), origin+"/log/")
		if err != nil {
			t.Fatalf("list %s's objects: %v", origin, err)
		}
		var wantBytes int64
		for _, o := range sizes {
			wantBytes += o.Size
		}
		if h.size != wantBytes {
			t.Errorf("heads %s bytes = %d, want the sum of its object sizes %d", origin, h.size, wantBytes)
		}
		lastKey := sizes[len(sizes)-1].Key
		if h.key != lastKey {
			t.Errorf("heads %s key = %q, want the last object's key %q", origin, h.key, lastKey)
		}
		if _, err := time.Parse(time.RFC3339Nano, h.at); err != nil {
			t.Errorf("heads %s at = %q, want an RFC3339Nano time: %v", origin, h.at, err)
		}
	}
	if h := readHeadsRow(t, path, "origin-a"); h.label != "laptop" {
		t.Errorf("heads origin-a label = %q, want the newest upsert's laptop", h.label)
	}
	if h := readHeadsRow(t, path, "origin-b"); h.label != "desk" {
		t.Errorf("heads origin-b label = %q, want desk", h.label)
	}
	if h := readHeadsRow(t, path, "origin-b"); h.floor != 5000 {
		t.Errorf("heads origin-b floor = %d, want the floor meta's 5000", h.floor)
	}
	if h := readHeadsRow(t, path, "origin-a"); h.floor != 0 {
		t.Errorf("heads origin-a floor = %d, want 0 without a floor row", h.floor)
	}
	if h := readHeadsRow(t, path, "origin-c"); h.label != "" {
		t.Errorf("heads origin-c label = %q, want empty with no installation row", h.label)
	}
	var wantShared []Entry
	for _, e := range seeded {
		if e.Tbl == "doc:config" || e.Tbl == "doc:secrets" {
			wantShared = append(wantShared, e)
		}
	}
	sort.Slice(wantShared, func(i, j int) bool {
		if wantShared[i].Origin != wantShared[j].Origin {
			return wantShared[i].Origin < wantShared[j].Origin
		}
		return wantShared[i].Seq < wantShared[j].Seq
	})
	assertMigratedEntries(t, readSharedRows(t, path), wantShared)
	if got := len(readSharedRows(t, path)); got != result.SharedRows {
		t.Errorf("shared rows = %d, want the result's %d", got, result.SharedRows)
	}
	if result.Rows != len(seeded) {
		t.Errorf("result rows = %d, want the %d seeded rows", result.Rows, len(seeded))
	}
	for origin, rows := range byOrigin {
		got, ok := result.Origins[origin]
		if !ok {
			t.Errorf("result has no origin %s", origin)
			continue
		}
		if got.Rows != len(rows) {
			t.Errorf("result %s rows = %d, want %d", origin, got.Rows, len(rows))
		}
		wantObjects := (len(rows) + 4095) / 4096
		if len(rows) > 0 && got.Objects != wantObjects {
			t.Errorf("result %s objects = %d, want ceil(%d/4096) = %d", origin, got.Objects, len(rows), wantObjects)
		}
		if got.Seq != sortedMax(rows) {
			t.Errorf("result %s seq = %d, want the max %d", origin, got.Seq, sortedMax(rows))
		}
		if got.LegacyUpto != sortedMax(rows) {
			t.Errorf("result %s legacy_upto = %d, want the max %d", origin, got.LegacyUpto, sortedMax(rows))
		}
	}
	if result.ObjectsCreated+result.ObjectsVerified != totalObjects(t, store) {
		t.Errorf("created %d + verified %d != the store's objects", result.ObjectsCreated, result.ObjectsVerified)
	}
	if result.RowsSwept != 0 {
		t.Errorf("rows swept = %d, want 0 on a clean run", result.RowsSwept)
	}
	if after, _ := metaRow(t, path, "signed_from.origin-a"); after != signedBefore {
		t.Errorf("signed_from.origin-a = %q, want the %q it held", after, signedBefore)
	}
	if after, _ := metaRow(t, path, "epoch_cut.origin-a"); after != cutBefore {
		t.Errorf("epoch_cut.origin-a = %q, want the %q it held", after, cutBefore)
	}
	if _, ok := metaRow(t, path, "floor.origin-b"); ok {
		t.Error("floor.origin-b survived the migration, want it dropped with heads carrying the floor")
	}
	copyPath := filepath.Join(t.TempDir(), "reader.db")
	if _, err := b.db.Exec("VACUUM INTO ?", copyPath); err != nil {
		t.Fatalf("copy the migrated replica: %v", err)
	}
	readerDriver := &localDriver{path: copyPath}
	reader := NewTursoBackend(readerDriver)
	reader.openStore = func(R2Config) blobstore.BlobStore { return store }
	if err := reader.Open(Spec{
		Version: ProtocolVersion, Origin: "origin-reader", URL: "libsql://scratch.example",
		Token: "the-token", R2: &R2Config{Bucket: "bucket"}, MirrorTables: migrateMirror,
	}); err != nil {
		t.Fatalf("open the reader: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	marks := map[string]int{}
	var pulled []Entry
	for i := 0; i < 10; i++ {
		page, err := reader.Pull(marks)
		if err != nil {
			t.Fatalf("pull %d: %v", i+1, err)
		}
		if len(page) == 0 {
			break
		}
		before := marksCopy(marks)
		for _, e := range page {
			if e.Seq > marks[e.Origin] {
				marks[e.Origin] = e.Seq
			}
		}
		if equalMarks(before, marks) {
			t.Fatalf("pull %d returned %d entries without advancing the marks", i+1, len(page))
		}
		pulled = append(pulled, page...)
		if i == 9 {
			t.Fatal("the backlog did not drain in 10 pulls")
		}
	}
	byPulled := make(map[string][]Entry)
	for _, e := range pulled {
		byPulled[e.Origin] = append(byPulled[e.Origin], e)
	}
	for origin, rows := range byOrigin {
		sorted := append([]Entry(nil), rows...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
		got := byPulled[origin]
		sort.Slice(got, func(i, j int) bool { return got[i].Seq < got[j].Seq })
		assertMigratedEntries(t, got, sorted)
	}
	ranged, err := reader.PullRange("origin-b", 4999, 5002)
	if err != nil {
		t.Fatalf("pull_range across the floor: %v", err)
	}
	assertSeqs(t, ranged, 5000, 5001, 5002)
	for origin := range byOrigin {
		rows := pageHead(t, reader, origin)
		if len(rows) == 0 {
			t.Errorf("head of %s came back empty", origin)
		}
	}
	maxA := 0
	for _, e := range byOrigin["origin-a"] {
		if e.Seq > maxA {
			maxA = e.Seq
		}
	}
	legacyBefore := readHeadsRow(t, path, "origin-a").legacyUpto
	written, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["after"]`, `{"title":"after"}`)})
	if err != nil {
		t.Fatalf("append after the migration: %v", err)
	}
	if len(written) != 1 || written[0].Seq != maxA+1 {
		t.Errorf("append seq = %+v, want one entry at %d", written, maxA+1)
	}
	if h := readHeadsRow(t, path, "origin-a"); h.legacyUpto != legacyBefore {
		t.Errorf("legacy_upto after the append = %d, want the migration's %d", h.legacyUpto, legacyBefore)
	}
	if driver.pushes < 2 {
		t.Errorf("pushes = %d, want at least the flip and the commit", driver.pushes)
	}
	_ = driver
}

func sortedMax(rows []Entry) int {
	max := 0
	for _, e := range rows {
		if e.Seq > max {
			max = e.Seq
		}
	}
	return max
}
