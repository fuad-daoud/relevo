package relevo

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The one-time archived feed ingests each archived record into the mirror,
// keyed by kv "ingested.archive.<recordID>", and a second pass changes nothing.
func TestMirrorArchivedFeedsOnce(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, _, err := db.SplitOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}
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
	if _, ok, err := d.Local().KVGet(key); err != nil || !ok {
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
	if stats, ran, err := db.SplitOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("second SplitOnce: %v", err)
	} else if ran {
		t.Errorf("the split pass moved %d kv keys the mirror left in the shared file, want none", stats.KVKeysMoved)
	}
}

// TestMirrorArchivedReingestsALegacyMark pins the one-time repair: a record
// whose mark holds any value but the current one is ingested once more, so a
// round row the earlier derivation left behind is re-derived from the stored
// log, and the mark moves to the current value. A further pass changes no
// row.
func TestMirrorArchivedReingestsALegacyMark(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, _, err := db.SplitOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}
	rt.DB = d

	const cand = "agy/google/gemini-3.8-flash-high"
	if err := rt.Store.Save(store.Binding{
		Name: "webshop", CWD: "/repo", Round: 1, State: store.StateDone,
		BuilderCandidate: cand, BuilderAccount: "cp2",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindPick, Confirmed: true,
		Note: "picked " + cand + " for builder: order #1; account cp2",
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
	recordID := archived[0].RecordID

	mirrorArchived(context.Background(), rt)

	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("Binding(webshop): found=%v err=%v", found, err)
	}
	rounds, err := d.Rounds(row.ID)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("Rounds = %+v, %v, want one round", rounds, err)
	}
	if rounds[0].Candidate == nil || *rounds[0].Candidate != cand {
		t.Fatalf("round Candidate = %v, want %s", rounds[0].Candidate, cand)
	}

	// Setup for the repair: the mark holds a non-current value, and the
	// row names a candidate the log contradicts.
	if err := d.Local().KVPut("ingested.archive."+recordID, []byte("true")); err != nil {
		t.Fatalf("KVPut legacy mark: %v", err)
	}
	stale := rounds[0]
	stale.Candidate = ptr("opencode/openrouter/z-ai/glm-5.3-flash")
	stale.Harness = ptr("opencode")
	stale.Provider = ptr("openrouter")
	stale.Model = ptr("z-ai/glm-5.3-flash")
	if _, err := d.UpsertRound(stale); err != nil {
		t.Fatalf("UpsertRound stale: %v", err)
	}

	mirrorArchived(context.Background(), rt)

	rounds, err = d.Rounds(row.ID)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("Rounds = %+v, %v, want one round", rounds, err)
	}
	if rounds[0].Candidate == nil || *rounds[0].Candidate != cand {
		t.Errorf("round Candidate = %v, want the pick's %s again", rounds[0].Candidate, cand)
	}
	if got, ok, err := d.Local().KVGet("ingested.archive." + recordID); err != nil || !ok || string(got) != strconv.Quote(archiveMarkVersion) {
		t.Errorf("mark = %q, ok=%v, err=%v; want the current value", got, ok, err)
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
			t.Errorf("a third pass changed Rows[%s]: before %d, after %d", tbl, n, after.Rows[tbl])
		}
	}
}
