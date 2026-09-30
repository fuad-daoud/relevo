package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestIngestStoreSourceFillsTheMirror pins that a binding held in the store's
// database ingests exactly as the same fixture did through DirSource, though
// bind.json and log.jsonl no longer exist as files.
func TestIngestStoreSourceFillsTheMirror(t *testing.T) {
	ctx := context.Background()
	deps := Deps{Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}

	dir := copyFixture(t)
	dirDB := openTestDB(t)
	if _, err := Ingest(ctx, DirSource(dir), dirDB, deps); err != nil {
		t.Fatalf("Ingest(DirSource): %v", err)
	}

	st := adoptIntoStore(t, dir)

	storeDB := openTestDB(t)
	if _, err := Ingest(ctx, StoreSource(st, "fixture"), storeDB, deps); err != nil {
		t.Fatalf("Ingest(StoreSource): %v", err)
	}

	dirBinding := mustBinding(t, dirDB, "fixture")
	storeBinding := mustBinding(t, storeDB, "fixture")
	if storeBinding.CWD != dirBinding.CWD || !strEq(storeBinding.FinalState, "needs_you") {
		t.Errorf("StoreSource binding = %+v, want DirSource's %+v", storeBinding, dirBinding)
	}

	dirEvents, err := dirDB.Events(dirBinding.ID, 0)
	if err != nil {
		t.Fatalf("Events(dir): %v", err)
	}
	storeEvents, err := storeDB.Events(storeBinding.ID, 0)
	if err != nil {
		t.Fatalf("Events(store): %v", err)
	}
	if len(storeEvents) == 0 || len(storeEvents) != len(dirEvents) {
		t.Fatalf("StoreSource ingested %d events, DirSource %d", len(storeEvents), len(dirEvents))
	}
	for i := range storeEvents {
		if got, want := entryOf(t, storeEvents[i].EntryJSON), entryOf(t, dirEvents[i].EntryJSON); got != want {
			t.Errorf("event %d = %s, want %s\nstore: %s\n  dir: %s",
				i, got, want, storeEvents[i].EntryJSON, dirEvents[i].EntryJSON)
		}
	}

	// Round files still come from the binding directory, not from Load.
	if rounds := mustRounds(t, storeDB, storeBinding.ID); len(rounds) != 3 {
		t.Errorf("StoreSource ingested %d rounds, want 3 (the round files)", len(rounds))
	}
}

// TestIngestAdoptsTheSessionMasterMindID pins the stale-id path: a binding
// naming a mastermind id the session no longer answers to (its record was
// forgotten and registered anew) mirrors under the id that holds the session,
// instead of failing every run with the natural-key refusal.
func TestIngestAdoptsTheSessionMasterMindID(t *testing.T) {
	ctx := context.Background()

	st := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo", State: store.StateActive, Round: 1}
	b.MasterMind.Kind = "claude"
	b.MasterMind.SessionID = "sess-live"
	b.MasterMindID = "pl_stale"
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d := openTestDB(t)
	if _, err := d.UpsertMasterMind(db.MasterMind{ID: "pl_live", HarnessKind: "claude", SessionID: "sess-live"}); err != nil {
		t.Fatalf("seed mastermind: %v", err)
	}
	if _, err := Ingest(ctx, StoreSource(st, "webshop"), d, Deps{}); err != nil {
		t.Fatalf("Ingest over a stale mastermind id: %v", err)
	}

	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("mirror Binding: found=%v err=%v", found, err)
	}
	if row.MasterMindID == nil || *row.MasterMindID != "pl_live" {
		t.Errorf("mirror mastermind id = %v, want pl_live (the session's own id)", row.MasterMindID)
	}
	if _, ok, err := d.MasterMindBySession("claude", "sess-live"); err != nil || !ok {
		t.Errorf("MasterMindBySession = ok %v, err %v; want the session's row", ok, err)
	}
}

// adoptIntoStore seeds dir's binding into a fresh store root's database and
// removes bind.json/log.jsonl, so the store's database becomes the binding's
// home and the files no longer exist.
func adoptIntoStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st := store.New(t.TempDir())
	if err := os.MkdirAll(st.Dir("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(st.Dir("fixture"), e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seedStoreRecord(t, st, "fixture", st.Dir("fixture"))
	for _, base := range []string{"bind.json", "log.jsonl"} {
		if err := os.Remove(filepath.Join(st.Dir("fixture"), base)); err != nil {
			t.Fatalf("remove %s: %v", base, err)
		}
		if _, err := os.Stat(filepath.Join(st.Dir("fixture"), base)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is still a file: %v", base, err)
		}
	}
	return st
}

// entryOf decodes an event's entry_json to the fields both sources must agree on;
// their bytes differ by construction, so the test compares meaning, not bytes.
func entryOf(t *testing.T, entryJSON string) string {
	t.Helper()
	var e store.LogEntry
	if err := json.Unmarshal([]byte(entryJSON), &e); err != nil {
		t.Fatalf("decode %s: %v", entryJSON, err)
	}
	return fmt.Sprintf("round=%d %s %s %s %q", e.Round, e.Direction, e.Kind, e.Path, e.Note)
}
