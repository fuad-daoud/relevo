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

// archiveTickRuntime is the rig for the archive-mirror tests: a runtime over
// a temp store and a temp shared file, with the daemon reading both.
func archiveTickRuntime(t *testing.T) (Runtime, *db.DB, *Daemon) {
	t.Helper()
	rt := newRuntime(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	rt.DB = d
	return rt, d, NewDaemon(rt, time.Second)
}

// seedLiveWebshop saves an active headless binding with one prompt line, a
// shape a tick reconciles without starting work.
func seedLiveWebshop(t *testing.T, rt Runtime) {
	t.Helper()
	if err := rt.Store.Save(store.Binding{
		Name: "webshop", CWD: "/repo", State: store.StateActive, Round: 1,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog("webshop", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
}

// TestTickMirrorsNewlyArchivedBinding pins the history symptom: after Save, a
// log line and Archive, one Tick leaves the row in the mirror with ArchivedAt
// set and history reporting the binding as archived. The mirrored row keeps
// the record's own state: the record is authoritative, and marking it done
// here would land unverified work in stats.
func TestTickMirrorsNewlyArchivedBinding(t *testing.T) {
	rt, d, daemon := archiveTickRuntime(t)
	seedLiveWebshop(t, rt)
	if _, err := rt.Store.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("mirror Binding: found=%v err=%v", found, err)
	}
	if row.ArchivedAt == nil {
		t.Error("mirrored row has no ArchivedAt, want the archive stamp")
	}
	final := ""
	if row.FinalState != nil {
		final = *row.FinalState
	}
	if final != string(store.StateActive) {
		t.Errorf("mirrored FinalState = %q, want the record's own %q", final, string(store.StateActive))
	}

	history, err := Bindings(context.Background(), rt, "")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	var hb *HistoryBinding
	for i := range history {
		if history[i].Name == "webshop" {
			hb = &history[i]
			break
		}
	}
	if hb == nil {
		t.Fatalf("history holds %d bindings, none named webshop", len(history))
	}
	if !hb.Archived {
		t.Error("history reports webshop as live, want archived")
	}
}

// TestTickMirrorsArchiveLandingAfterAnEarlierTick pins the every-tick
// cadence: an archive that lands after a tick is mirrored by the next tick,
// with no restart in between.
func TestTickMirrorsArchiveLandingAfterAnEarlierTick(t *testing.T) {
	rt, d, daemon := archiveTickRuntime(t)
	seedLiveWebshop(t, rt)

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if _, err := rt.Store.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("mirror Binding after the second tick: found=%v err=%v", found, err)
	}
	if row.ArchivedAt == nil {
		t.Error("mirrored row has no ArchivedAt after the archive landed between ticks")
	}
}

// TestTickMirrorsBindingArchivedBeforeAnyTick pins the missing-binding case:
// a record archived with no log lines before its first tick still reaches the
// mirror on that tick, without ever needing a live sighting.
func TestTickMirrorsBindingArchivedBeforeAnyTick(t *testing.T) {
	rt, d, daemon := archiveTickRuntime(t)
	if err := rt.Store.Save(store.Binding{Name: "webshop", CWD: "/repo", State: store.StateActive, Round: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := rt.Store.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("mirror Binding: found=%v err=%v", found, err)
	}
	if row.ArchivedAt == nil {
		t.Error("mirrored row has no ArchivedAt, want the archive stamp")
	}
}
