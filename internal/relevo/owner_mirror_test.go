package relevo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

func ownerMirrorRuntime(t *testing.T) (Runtime, *db.DB) {
	t.Helper()
	rt := newRuntime(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	rt.DB = d
	if err := rt.Store.Save(store.Binding{Name: "webshop", CWD: "/repo", State: store.StateActive, Round: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog("webshop", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	return rt, d
}

// TestOwnerMirrorSkipsUnchangedAcrossPasses pins what the mirror exists for: a
// pass with nothing new leaves the log cursor alone, because a pass that ran
// ingest would replace it, and a pass after a new line mirrors that line.
func TestOwnerMirrorSkipsUnchangedAcrossPasses(t *testing.T) {
	rt, d := ownerMirrorRuntime(t)
	m := NewOwnerMirror(rt, time.Now())
	ctx := context.Background()
	key := filepath.Join(rt.Store.Dir("webshop"), "log.jsonl")

	m.Pass(ctx)
	first, found, err := d.Cursor(key)
	if err != nil || !found {
		t.Fatalf("cursor after the first pass: found=%v err=%v", found, err)
	}

	time.Sleep(5 * time.Millisecond)
	m.Pass(ctx)
	second, _, err := d.Cursor(key)
	if err != nil {
		t.Fatalf("cursor after the idle pass: %v", err)
	}
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("idle pass rewrote the cursor at %v, want it untouched at %v", second.UpdatedAt, first.UpdatedAt)
	}

	if err := rt.Store.AppendLog("webshop", store.LogEntry{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	m.Pass(ctx)
	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("mirror Binding: found=%v err=%v", found, err)
	}
	if events, err := d.Events(row.ID, 0); err != nil || len(events) != 2 {
		t.Errorf("mirrored events = %d, %v; want the new line landed beside the first", len(events), err)
	}
}

// TestOwnerMirrorTakesOnlyRecordsArchivedAfterItStarted: the past stays out and
// no pass walks all of it, while a binding archived after the start still lands.
func TestOwnerMirrorTakesOnlyRecordsArchivedAfterItStarted(t *testing.T) {
	rt, _ := ownerMirrorRuntime(t)
	since := time.Now()
	fake := &fakeArchiveBackend{
		recs: []store.ArchivedRef{
			{RecordID: "old", Name: "old", ArchivedAt: since.Add(-2 * time.Hour)},
			{RecordID: "new", Name: "new", ArchivedAt: since.Add(time.Minute)},
		},
		ingested: map[string]bool{},
	}
	counting := &countingArchiveBackend{archiveBackend: fake}
	m := NewOwnerMirror(rt, since)
	m.d.archiveBackend = counting

	m.Pass(context.Background())

	if counting.listAllCalls != 0 {
		t.Errorf("listAll calls = %d, want none: a full listing every start walks the whole past", counting.listAllCalls)
	}
	if counting.ingestCalls != 1 || !fake.ingested["new"] {
		t.Errorf("ingest calls = %d, ingested = %v; want only the record archived after the start", counting.ingestCalls, fake.ingested)
	}
}

// TestOwnerMirrorStopsWhenCancelled: a server shutting down waits for the pass,
// so a pass must not go on to ingest the rest of the owner's bindings.
func TestOwnerMirrorStopsWhenCancelled(t *testing.T) {
	rt, d := ownerMirrorRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	NewOwnerMirror(rt, time.Now()).Pass(ctx)

	if _, found, err := d.Binding("webshop"); err != nil || found {
		t.Errorf("mirror Binding after a cancelled pass: found=%v err=%v; want nothing written", found, err)
	}
}
