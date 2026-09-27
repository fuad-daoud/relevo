package relevo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestMirrorArchivedFeedsOnce pins P3d §4.5: the one-time archived feed
// ingests each archived record into the mirror, keyed by kv
// "ingested.archive.<recordID>", and a second pass changes nothing.
func TestMirrorArchivedFeedsOnce(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	rt.DB = d

	if err := rt.Store.Save(store.Binding{Name: "webshop", CWD: "/repo", Round: 2, State: store.StateDone}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if _, err := rt.Store.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := rt.Store.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}

	mirrorArchived(context.Background(), rt)

	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("Binding(webshop): found=%v err=%v", found, err)
	}
	if row.ArchivedAt == nil {
		t.Error("the mirror binding has no ArchivedAt, want the archive stamp")
	}
	key := "ingested.archive." + archived[0].RecordID
	if _, ok, err := d.KVGet(key); err != nil || !ok {
		t.Errorf("kv %s = ok %v, err %v; want it set", key, ok, err)
	}

	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	mirrorArchived(context.Background(), rt)
	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for tbl, n := range before.Rows {
		if after.Rows[tbl] != n {
			t.Errorf("a second pass changed Rows[%s]: before %d, after %d", tbl, n, after.Rows[tbl])
		}
	}
}
