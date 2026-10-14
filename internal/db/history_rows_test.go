package db

import (
	"path/filepath"
	"testing"
	"time"
)

// The history tables' own seam: the two conversions, the two inserts, and the
// age prune. Every case here goes through this package's surface rather than
// through a caller of it, because the values are the part a caller cannot check
// for itself: a time stored as text orders two events wrongly, and a prune that
// compared the wrong column would remove a row inside the window.

func openHistoryDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenWith(filepath.Join(t.TempDir(), "relevo.db"), Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// The time column is an integer, and it orders two events that an RFC3339
// rendering misorders: the same instant written in two offsets.
func TestUnixMSOrdersWhatTextMisorders(t *testing.T) {
	t.Parallel()
	east := time.Date(2026, time.October, 14, 10, 0, 0, 0, time.FixedZone("+03:00", 3*3600))
	utc := time.Date(2026, time.October, 14, 7, 0, 0, 0, time.UTC)
	if UnixMS(east) != UnixMS(utc) {
		t.Fatalf("UnixMS of %s = %d and of %s = %d, want the same instant",
			east, UnixMS(east), utc, UnixMS(utc))
	}
	if got := UnixMS(east); got <= UnixMS(east.Add(-time.Millisecond)) {
		t.Fatalf("UnixMS = %d, want a millisecond before it to be smaller", got)
	}
	if got := TimeFromUnixMS(UnixMS(east)); !got.Equal(east.UTC()) {
		t.Fatalf("TimeFromUnixMS = %s, want %s", got, east.UTC())
	}
	// A row with no time reads as a row with no time rather than as 1970.
	if got := TimeFromUnixMS(0); !got.IsZero() {
		t.Fatalf("TimeFromUnixMS(0) = %s, want the zero time", got)
	}
}

func TestInsertAvailabilityEventWritesOneRow(t *testing.T) {
	t.Parallel()
	d := openHistoryDB(t)
	at := time.Date(2026, time.October, 14, 9, 0, 0, 0, time.UTC)
	since := at.Add(-2 * time.Hour)
	for _, row := range []AvailabilityEvent{
		{ID: NewID(), AtMS: UnixMS(at), Kind: "rate_limited", Provider: "test", Source: "relevo"},
		{ID: NewID(), AtMS: UnixMS(at), Kind: "cleared", Provider: "test", Source: "relevo", SinceMS: ptr(UnixMS(since))},
	} {
		if err := d.InsertAvailabilityEvent("m2", row); err != nil {
			t.Fatalf("InsertAvailabilityEvent(%s): %v", row.ID, err)
		}
	}
	// The owner is the caller's argument, not a column the row carried: a row
	// written under a peer id is stamped the way the caller said, which is what
	// makes the walk find it under that peer.
	page, err := d.SharedOwnedRowPage(AvailabilityEventTable, "m2", 0, 10)
	if err != nil {
		t.Fatalf("SharedOwnedRowPage: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("the file holds %d availability events of m2, want 2", len(page))
	}
}

func TestInsertLatencySampleWritesOneRow(t *testing.T) {
	t.Parallel()
	d := openHistoryDB(t)
	at := time.Date(2026, time.October, 14, 9, 0, 0, 0, time.UTC)
	row := LatencySample{ID: NewID(), AtMS: UnixMS(at), Token: "claude/test/m", Host: "laptop", TTFTMS: 120, TotalMS: 900}
	if err := d.Tx(func(tx *Tx) error { return tx.insertLatencySample("m1", row) }); err != nil {
		t.Fatalf("insertLatencySample: %v", err)
	}
	page, err := d.SharedOwnedRowPage(LatencySampleTable, "m1", 0, 10)
	if err != nil {
		t.Fatalf("SharedOwnedRowPage: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("the file holds %d latency samples of m1, want 1", len(page))
	}
	atMS, ok := HistoryAtMS(page[0])
	if !ok || atMS != UnixMS(at) {
		t.Fatalf("HistoryAtMS = (%d, %t), want (%d, true)", atMS, ok, UnixMS(at))
	}
}

// The prune is one comparison against the time column, for every origin, and it
// leaves the rest of the table alone.
func TestPruneHistoryDeletesAgedRowsOfEveryOrigin(t *testing.T) {
	t.Parallel()
	d := openHistoryDB(t)
	cutoff := UnixMS(time.Date(2026, time.October, 14, 9, 0, 0, 0, time.UTC))
	aged, live := cutoff-1, cutoff+1
	for _, origin := range []string{"m1", "m2"} {
		for _, at := range []int64{aged, live} {
			event := AvailabilityEvent{
				ID: NewID(), AtMS: at, Kind: "rate_limited", Provider: "test", Source: "relevo",
			}
			if err := d.InsertAvailabilityEvent(origin, event); err != nil {
				t.Fatalf("InsertAvailabilityEvent: %v", err)
			}
			sample := LatencySample{ID: NewID(), AtMS: at, Token: "claude/test/m", TTFTMS: 1, TotalMS: 2}
			if err := d.InsertLatencySample(origin, sample); err != nil {
				t.Fatalf("InsertLatencySample: %v", err)
			}
		}
	}

	pruned, err := d.PruneHistory(cutoff)
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if pruned.Events != 2 || pruned.Samples != 2 {
		t.Fatalf("pruned = %+v, want one aged row of each table per origin", pruned)
	}
	for _, tbl := range []string{AvailabilityEventTable, LatencySampleTable} {
		for _, origin := range []string{"m1", "m2"} {
			page, err := d.SharedOwnedRowPage(tbl, origin, 0, 10)
			if err != nil {
				t.Fatalf("SharedOwnedRowPage %s %s: %v", tbl, origin, err)
			}
			if len(page) != 1 {
				t.Fatalf("%s holds %d rows of %s, want the one inside the window", tbl, len(page), origin)
			}
		}
	}
	// A second pass is a no-op rather than an error: the daemon runs it hourly
	// whether or not anything aged out.
	if again, err := d.PruneHistory(cutoff); err != nil || again != (HistoryPruned{}) {
		t.Fatalf("second PruneHistory = (%+v, %v), want nothing left to remove", again, err)
	}
}

// A row this machine already holds is a caller's own failure rather than a
// refusal the log's answer would cover, so the insert names the row it could
// not write. Nothing about the age prune reaches this path: these tables are
// append-only, so a row is written once or not at all.
func TestInsertHistoryRowRefusesASecondWrite(t *testing.T) {
	t.Parallel()
	d := openHistoryDB(t)
	at := UnixMS(time.Now())
	event := AvailabilityEvent{
		ID: NewID(), AtMS: at, Kind: "rate_limited", Provider: "test", Source: "relevo",
	}
	if err := d.InsertAvailabilityEvent("m1", event); err != nil {
		t.Fatalf("InsertAvailabilityEvent: %v", err)
	}
	if err := d.InsertAvailabilityEvent("m1", event); err == nil {
		t.Fatal("InsertAvailabilityEvent wrote the same id twice, want the second refused")
	}
	sample := LatencySample{ID: event.ID, AtMS: at, Token: "claude/test/m", TTFTMS: 1, TotalMS: 2}
	if err := d.InsertLatencySample("m1", sample); err != nil {
		t.Fatalf("InsertLatencySample: %v", err)
	}
	if err := d.InsertLatencySample("m1", sample); err == nil {
		t.Fatal("InsertLatencySample wrote the same id twice, want the second refused")
	}
}

// The migration's insert path is the one caller that needs a second write of the
// same id tolerated: a pass that crashed between its row writes and its marker is
// retried and offers the ids the crashed pass already wrote. The live path
// refuses that write -- see TestInsertHistoryRowRefusesASecondWrite, which stands
// unchanged -- so the tolerance lives on the migration's own path.
//
// An ignored duplicate inserts no row, so the outbox trigger beside the table
// does not fire for it. That is asserted rather than assumed: it is the property
// that makes OR IGNORE safe here rather than merely convenient, because a
// duplicate outbox entry is a row a peer would apply twice.
func TestMigratedHistoryRowIgnoresASecondWrite(t *testing.T) {
	t.Parallel()
	d := openHistoryDB(t)
	at := UnixMS(time.Now())
	event := AvailabilityEvent{
		ID: NewID(), AtMS: at, Kind: "rate_limited", Provider: "test", Source: "relevo",
	}
	if err := d.InsertMigratedAvailabilityEvent("m1", event); err != nil {
		t.Fatalf("first InsertMigratedAvailabilityEvent: %v", err)
	}
	if err := d.InsertMigratedAvailabilityEvent("m1", event); err != nil {
		t.Fatalf("the retry of InsertMigratedAvailabilityEvent failed on a row already there: %v", err)
	}
	sample := LatencySample{ID: event.ID, AtMS: at, Token: "claude/test/m", TTFTMS: 1, TotalMS: 2}
	if err := d.InsertMigratedLatencySample("m1", sample); err != nil {
		t.Fatalf("first InsertMigratedLatencySample: %v", err)
	}
	if err := d.InsertMigratedLatencySample("m1", sample); err != nil {
		t.Fatalf("the retry of InsertMigratedLatencySample failed on a row already there: %v", err)
	}

	// One row each, not two: the retry changed nothing.
	if got := queryStrings(t, d, `SELECT id FROM `+AvailabilityEventTable); len(got) != 1 {
		t.Errorf("availability_event holds %v, want the single row the first write left", got)
	}
	if got := queryStrings(t, d, `SELECT id FROM `+LatencySampleTable); len(got) != 1 {
		t.Errorf("latency_sample holds %v, want the single row the first write left", got)
	}

	// And one outbox entry each: the trigger fires on a row actually inserted,
	// so the ignored duplicate records nothing a peer would apply a second time.
	for _, tbl := range []string{AvailabilityEventTable, LatencySampleTable} {
		if got := queryStrings(t, d, `SELECT tbl FROM sync_outbox WHERE tbl = ?`, tbl); len(got) != 1 {
			t.Errorf("sync_outbox holds %d entries for %s, want the one the real write recorded", len(got), tbl)
		}
	}
}

// A body's time is read out of what it carries, and a body that carries none is
// not judged: the column is NOT NULL at write time, so the window has nothing
// to compare.
func TestHistoryAtReadsOnlyTheTimeColumn(t *testing.T) {
	t.Parallel()
	values := map[string]any{"id": "01A", "at_ms": int64(1757000000000)}
	if got, ok := HistoryAt(AvailabilityEventTable, values); !ok || got != 1757000000000 {
		t.Fatalf("HistoryAt = (%d, %t), want the body's time", got, ok)
	}
	for _, v := range []any{float64(1757000000000), int(1757000000000)} {
		if got, ok := HistoryAt(LatencySampleTable, map[string]any{"at_ms": v}); !ok || got != 1757000000000 {
			t.Fatalf("HistoryAt of %T = (%d, %t), want the body's time", v, got, ok)
		}
	}
	if _, ok := HistoryAt(AvailabilityEventTable, map[string]any{"id": "01A"}); ok {
		t.Fatal("HistoryAt of a body with no time = ok, want it left alone")
	}
	if _, ok := HistoryAt("binding", values); ok {
		t.Fatal("HistoryAt of a table that is not append-only = ok, want it left alone")
	}
	if _, ok := HistoryAtMS(ExchangeRow{Columns: []ExchangeColumn{{Name: "id", Value: "01A"}}}); ok {
		t.Fatal("HistoryAtMS of a row with no time column = ok, want it left alone")
	}
}
