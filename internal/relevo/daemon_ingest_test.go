package relevo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestIngestLiveBindingsSkipsUnchanged pins the daemon's change check: a tick
// with nothing new leaves the mirror's cursor alone, and a tick after a new
// log line mirrors it. Without the check the first is a whole transaction per
// binding every two seconds.
func TestIngestLiveBindingsSkipsUnchanged(t *testing.T) {
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

	daemon := NewDaemon(rt, time.Second)
	bindings, err := rt.Store.List()
	if err != nil || len(bindings) != 1 {
		t.Fatalf("List = %d bindings, %v; want one", len(bindings), err)
	}
	cursorKey := filepath.Join(rt.Store.Dir("webshop"), "log.jsonl")

	daemon.ingestLiveBindings(context.Background(), bindings)
	mirror, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("mirror Binding: found=%v err=%v", found, err)
	}
	if events, err := d.Events(mirror.ID, 0); err != nil || len(events) != 1 {
		t.Fatalf("mirrored events = %d, %v; want one", len(events), err)
	}
	first, found, err := d.Cursor(cursorKey)
	if err != nil || !found {
		t.Fatalf("mirror cursor: found=%v err=%v", found, err)
	}

	// Nothing changed: the second tick must not run the mirror at all. The
	// cursor's own stamp is the observable -- a run replaces it.
	time.Sleep(5 * time.Millisecond)
	daemon.ingestLiveBindings(context.Background(), bindings)
	second, _, err := d.Cursor(cursorKey)
	if err != nil {
		t.Fatalf("cursor after the idle tick: %v", err)
	}
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("idle tick rewrote the cursor at %v, want it untouched at %v", second.UpdatedAt, first.UpdatedAt)
	}

	// A new log line changes the revision, so the next tick mirrors it.
	if err := rt.Store.AppendLog("webshop", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindAnswer}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	daemon.ingestLiveBindings(context.Background(), bindings)
	if events, err := d.Events(mirror.ID, 0); err != nil || len(events) != 2 {
		t.Fatalf("mirrored events after the append = %d, %v; want two", len(events), err)
	}

	if _, ok := daemon.ingestSeen["webshop"]; !ok {
		t.Error("ingestSeen has no entry for a binding that was just mirrored")
	}
	daemon.ingestLiveBindings(context.Background(), nil)
	if len(daemon.ingestSeen) != 0 {
		t.Errorf("ingestSeen = %v after a tick with no bindings, want empty", daemon.ingestSeen)
	}
}
