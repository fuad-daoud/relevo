package relevo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The history retention pass as the daemon runs it: on the maintenance tick
// beside the mastermind prune, at most once an hour, deleting every origin's
// aged rows and recording nothing.

// historyEvent inserts one availability event at the given millisecond under
// the named origin, the way a writer on that machine would.
func historyEvent(t *testing.T, d *db.DB, origin string, atMS int64) {
	t.Helper()
	row := db.AvailabilityEvent{
		ID: db.NewID(), AtMS: atMS, Kind: "rate_limited", Provider: "test", Source: "relevo",
	}
	if err := d.InsertAvailabilityEvent(origin, row); err != nil {
		t.Fatalf("insert availability_event: %v", err)
	}
}

// historyCount is how many availability_event rows the file holds.
func historyCount(t *testing.T, d *db.DB) int {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	var n int
	if err := raw.QueryRow(`SELECT count(*) FROM availability_event`).Scan(&n); err != nil {
		t.Fatalf("count availability_event: %v", err)
	}
	return n
}

// historyDaemonFile is a machine's shared file with one aged row and one live
// row, and a Runtime over it at a fixed clock.
func historyDaemonFile(t *testing.T) (Runtime, *db.DB, time.Time) {
	t.Helper()
	d, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("open the shared file: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	historyEvent(t, d, "m1", db.UnixMS(now.Add(-db.HistoryWindow-time.Hour)))
	historyEvent(t, d, "m1", db.UnixMS(now.Add(-time.Hour)))
	// A peer's row ages out here too: a machine shows a window of everything it
	// holds, not a window of what it wrote.
	historyEvent(t, d, "m2", db.UnixMS(now.Add(-db.HistoryWindow-time.Hour)))
	return Runtime{DB: d, Now: func() time.Time { return now }}, d, now
}

// The prune is reached from the daemon's maintenance, not only when called: a
// tick on a machine with no bindings still deletes the aged rows.
func TestTickPrunesAgedHistoryRows(t *testing.T) {
	t.Parallel()
	rt, d, _ := historyDaemonFile(t)
	rt.Store = store.New(t.TempDir())

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := historyCount(t, d); n != 1 {
		t.Fatalf("availability_event holds %d rows after a tick, want the one row inside the window", n)
	}
}

func TestDaemonPrunesAgedHistoryRows(t *testing.T) {
	t.Parallel()
	rt, d, now := historyDaemonFile(t)

	NewDaemon(rt, time.Second).pruneHistory()

	// One row is left: this machine's own, inside the window.
	if n := historyCount(t, d); n != 1 {
		t.Fatalf("availability_event holds %d rows, want the one row inside the window", n)
	}
	last, ok, err := historyLastPruned(d.LocalOrSelf())
	if err != nil || !ok || !last.Equal(now) {
		t.Fatalf("historyLastPruned = (%v, %t, %v), want (%v, true)", last, ok, err, now)
	}

	// The once-an-hour rule: a row that ages out in the next minute stays until
	// the next pass, because retention is not work a tick does.
	historyEvent(t, d, "m1", db.UnixMS(now.Add(-db.HistoryWindow-2*time.Hour)))
	NewDaemon(rt, time.Second).pruneHistory()
	if n := historyCount(t, d); n != 2 {
		t.Fatalf("availability_event holds %d rows, want the second prune held off by the hourly rule", n)
	}
	if !historyPruneDue(now, true, now.Add(historyPruneInterval)) {
		t.Fatal("historyPruneDue = false at exactly one interval, want the pass due again")
	}
}

// A Runtime with no shared file prunes nothing and records no marker, rather
// than failing a tick.
func TestDaemonPruneWithoutASharedFileIsQuiet(t *testing.T) {
	t.Parallel()
	NewDaemon(Runtime{Now: time.Now}, time.Second).pruneHistory()
}

// A prune that cannot read its marker leaves the marker where it was, so the
// next tick tries again instead of waiting out the hour.
func TestHistoryPruneDueNeedsNoMarker(t *testing.T) {
	t.Parallel()
	if !historyPruneDue(time.Time{}, false, time.Now()) {
		t.Fatal("historyPruneDue = false with no marker, want the first pass due at once")
	}
	if historyPruneDue(time.Now(), true, time.Now()) {
		t.Fatal("historyPruneDue = true a moment after a pass, want the next one held off")
	}
}
