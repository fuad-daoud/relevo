package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// logReadRuntime is a store with a fixed clock, for the tests that count and
// derive from one binding's log.
func logReadRuntime(t *testing.T, names ...string) Runtime {
	t.Helper()
	rt := routeRuntime(t)
	for _, name := range names {
		b := store.Binding{Name: name, CWD: "/repo/" + name, Round: 1, State: store.StateActive}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
	}
	return rt
}

// appendEntries writes entries in argument order, so the log's Seq order is
// the order the test declares.
func appendEntries(t *testing.T, rt Runtime, name string, entries ...store.LogEntry) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, e := range entries {
			if err := tx.AppendLog(name, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append log for %s: %v", name, err)
	}
}

func openActiveBinding(name string) store.Binding {
	return store.Binding{Name: name, CWD: "/repo/" + name, Round: 1, State: store.StateActive}
}

func statusOrFail(t *testing.T, rt Runtime) []view.BindingStatus {
	t.Helper()
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return rep.Bindings
}

// TestPendingIsDerivedFromTheHeldEntries pins that Pending is the OLDEST
// unconfirmed to-mastermind payload, derived from the log the row already
// decoded: the fixture's oldest unconfirmed entry is a findings payload and
// the newest is a report, so a scan that keeps overwriting names the wrong
// one.
func TestPendingIsDerivedFromTheHeldEntries(t *testing.T) {
	rt := logReadRuntime(t, "webshop")
	older := baseTime.Add(time.Minute)
	newer := baseTime.Add(2 * time.Minute)
	appendEntries(t, rt, "webshop",
		store.LogEntry{TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		store.LogEntry{TS: older, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindFindings, Payload: "older findings"},
		store.LogEntry{TS: newer, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "newer report"},
	)

	row, err := statusRow(context.Background(), rt, openActiveBinding("webshop"), statusConfig{detail: true})
	if err != nil {
		t.Fatalf("statusRow: %v", err)
	}
	if row.Pending == nil {
		t.Fatal("Pending = nil, want the oldest unconfirmed payload")
	}
	if row.Pending.Kind != store.KindFindings || !row.Pending.TS.Equal(older) {
		t.Errorf("Pending = %+v, want the older findings entry {round 1, findings, %v}", row.Pending, older)
	}
}

// TestUnreadIsUnchangedWhenViewedAtComesFromTheListRecord pins that the unread
// marker reads the stamp the List record already carried, and that it reads
// the same verdict the store's own stamp would: older than the report is
// unread, newer is read.
func TestUnreadIsUnchangedWhenViewedAtComesFromTheListRecord(t *testing.T) {
	rt := logReadRuntime(t, "webshop")
	reportTS := baseTime.Add(time.Minute)
	appendEntries(t, rt, "webshop", store.LogEntry{
		TS: reportTS, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
	})

	if _, ok := rt.Store.ViewedAt("webshop"); ok {
		t.Fatal("fixture carries a viewed stamp before one was written")
	}
	if err := rt.Store.MarkViewed("webshop", reportTS.Add(-time.Minute)); err != nil {
		t.Fatalf("MarkViewed: %v", err)
	}
	if rows := statusOrFail(t, rt); len(rows) != 1 || !rows[0].Unread {
		t.Errorf("rows = %+v, want the one row unread when the report is newer than the stamp", rows)
	}

	if err := rt.Store.MarkViewed("webshop", reportTS.Add(time.Minute)); err != nil {
		t.Fatalf("MarkViewed: %v", err)
	}
	if rows := statusOrFail(t, rt); len(rows) != 1 || rows[0].Unread {
		t.Errorf("rows = %+v, want the one row read when the stamp is newer than the report", rows)
	}

	// The binding List/Load hands the row carries the very stamp the store
	// still holds, which is what makes the two reads one.
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	at, ok := rt.Store.ViewedAt("webshop")
	if !ok || b.ViewedAt == nil || !b.ViewedAt.Equal(at) {
		t.Errorf("binding stamp = %v (ok %v), store stamp = %v (ok %v), want the same", b.ViewedAt, b.ViewedAt != nil, at, ok)
	}
}

// TestUnreadIsTrueWithNoViewedStamp is the no-stamp-at-all case: a report
// exists and nothing was ever viewed.
func TestUnreadIsTrueWithNoViewedStamp(t *testing.T) {
	rt := logReadRuntime(t, "webshop")
	appendEntries(t, rt, "webshop", store.LogEntry{
		TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
	})

	if rows := statusOrFail(t, rt); len(rows) != 1 || !rows[0].Unread {
		t.Errorf("rows = %+v, want the one row unread with no viewed stamp", rows)
	}
}

// TestStatusReadsEachLogOnce pins the phase's whole point: however many facts
// a row derives from its log, the log is decoded once. A row that fell back to
// PendingForMasterMind would decode it twice.
func TestStatusReadsEachLogOnce(t *testing.T) {
	names := []string{"a", "b", "c"}
	rt := logReadRuntime(t, names...)
	for _, name := range names {
		appendEntries(t, rt, name, store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		})
	}

	reads := map[string]int{}
	rt.Store.SetReadLogHook(func(name string) { reads[name]++ })

	if _, err := Status(context.Background(), rt); err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, name := range names {
		if reads[name] != 1 {
			t.Errorf("%s: %d log reads, want 1", name, reads[name])
		}
	}
}
