package db

import (
	"testing"
)

// The read seam of the two history tables. Every case here says what a reader
// sees rather than what the writer wrote: a read combines origins, takes the
// cutoff rather than a clock, and orders by at_ms so two machines reading the
// same file agree.

// seedHistoryRow writes one row of one history table under the named origin, so
// a case can build a file holding both its own rows and a peer's.
func seedHistoryRow(t *testing.T, d *DB, origin string, event AvailabilityEvent, sample LatencySample) {
	t.Helper()
	if err := d.InsertAvailabilityEvent(origin, event); err != nil {
		t.Fatalf("InsertAvailabilityEvent: %v", err)
	}
	if err := d.InsertLatencySample(origin, sample); err != nil {
		t.Fatalf("InsertLatencySample: %v", err)
	}
}

func TestHistoryReadsCombineOrigins(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	own := AvailabilityEvent{ID: NewID(), AtMS: 2000, Kind: "rate_limited", Provider: "own"}
	peer := AvailabilityEvent{ID: NewID(), AtMS: 1000, Kind: "rate_limited", Provider: "peer"}
	seedHistoryRow(t, d, "own-origin", own,
		LatencySample{ID: NewID(), AtMS: 2000, Token: "own/token", Host: "here", TTFTMS: 100, TotalMS: 200})
	seedHistoryRow(t, d, "peer-origin", peer,
		LatencySample{ID: NewID(), AtMS: 1000, Token: "peer/token", Host: "there", TTFTMS: 900, TotalMS: 1200})

	events, err := d.AvailabilityEvents(0)
	if err != nil {
		t.Fatalf("AvailabilityEvents: %v", err)
	}
	// Both origins, oldest first. A read scoped to the reading machine's own
	// origin would return one row and lose the fleet's window.
	if len(events) != 2 {
		t.Fatalf("AvailabilityEvents returned %d rows, want both origins': %+v", len(events), events)
	}
	if events[0].ID != peer.ID || events[1].ID != own.ID {
		t.Errorf("events = [%s %s], want oldest first", events[0].ID, events[1].ID)
	}
	if events[0].Provider != "peer" || events[1].Provider != "own" {
		t.Errorf("providers = [%s %s], want the peer's older row first", events[0].Provider, events[1].Provider)
	}

	samples, err := d.LatencySamples(0)
	if err != nil {
		t.Fatalf("LatencySamples: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("LatencySamples returned %d rows, want both origins': %+v", len(samples), samples)
	}
	if samples[0].Token != "peer/token" || samples[1].Token != "own/token" {
		t.Errorf("tokens = [%s %s], want oldest first", samples[0].Token, samples[1].Token)
	}
}

func TestHistoryReadsTakeTheCutoff(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	seedHistoryRow(t, d, "m1",
		AvailabilityEvent{ID: NewID(), AtMS: 500, Kind: "rate_limited", Provider: "aged"},
		LatencySample{ID: NewID(), AtMS: 500, Token: "aged/token", TTFTMS: 1, TotalMS: 2})
	seedHistoryRow(t, d, "m1",
		AvailabilityEvent{ID: NewID(), AtMS: 1500, Kind: "rate_limited", Provider: "live"},
		LatencySample{ID: NewID(), AtMS: 1500, Token: "live/token", TTFTMS: 3, TotalMS: 4})

	// The boundary is inclusive: the row at the cutoff is inside the window, the
	// one a millisecond before it is not.
	events, err := d.AvailabilityEvents(1000)
	if err != nil {
		t.Fatalf("AvailabilityEvents: %v", err)
	}
	if len(events) != 1 || events[0].Provider != "live" {
		t.Fatalf("events at cutoff 1000 = %+v, want only the live row", events)
	}
	samples, err := d.LatencySamples(1000)
	if err != nil {
		t.Fatalf("LatencySamples: %v", err)
	}
	if len(samples) != 1 || samples[0].Token != "live/token" {
		t.Fatalf("samples at cutoff 1000 = %+v, want only the live sample", samples)
	}

	// A cutoff below every row returns them all; the read is the cutoff's
	// decision alone and carries no window of its own.
	all, err := d.AvailabilityEvents(0)
	if err != nil {
		t.Fatalf("AvailabilityEvents(0): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("events at cutoff 0 = %d, want 2", len(all))
	}
}

// A nil since_ms is a nil SinceMS: it is a field only a clear carries, and a
// row that has one must not report a zero time as if it did.
func TestAvailabilityEventsReadsSinceOnlyWhenPresent(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	since := int64(900)
	if err := d.InsertAvailabilityEvent("m1", AvailabilityEvent{
		ID: NewID(), AtMS: 2000, Kind: "cleared", Provider: "test", SinceMS: &since,
	}); err != nil {
		t.Fatalf("InsertAvailabilityEvent(cleared): %v", err)
	}
	if err := d.InsertAvailabilityEvent("m1", AvailabilityEvent{
		ID: NewID(), AtMS: 1000, Kind: "rate_limited", Provider: "test",
	}); err != nil {
		t.Fatalf("InsertAvailabilityEvent(rate_limited): %v", err)
	}

	events, err := d.AvailabilityEvents(0)
	if err != nil {
		t.Fatalf("AvailabilityEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].SinceMS != nil {
		t.Errorf("the rate_limited row's since_ms = %v, want nil", events[0].SinceMS)
	}
	if events[1].SinceMS == nil || *events[1].SinceMS != since {
		t.Errorf("the cleared row's since_ms = %v, want %d", events[1].SinceMS, since)
	}
}

// CountHistoryRows is how the file is measured without reading it, so it must
// count every origin's rows and agree with the reads it stands in for.
func TestCountHistoryRowsCountsEveryOrigin(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	if got, err := d.CountHistoryRows(); err != nil || got != (HistoryPruned{}) {
		t.Fatalf("CountHistoryRows on an empty file = (%+v, %v), want nothing", got, err)
	}

	seedHistoryRow(t, d, "m1",
		AvailabilityEvent{ID: NewID(), AtMS: 1000, Kind: "rate_limited"},
		LatencySample{ID: NewID(), AtMS: 1000, Token: "a/token"})
	seedHistoryRow(t, d, "m2",
		AvailabilityEvent{ID: NewID(), AtMS: 2000, Kind: "rate_limited"},
		LatencySample{ID: NewID(), AtMS: 2000, Token: "b/token"})

	got, err := d.CountHistoryRows()
	if err != nil {
		t.Fatalf("CountHistoryRows: %v", err)
	}
	if got.Events != 2 || got.Samples != 2 {
		t.Errorf("CountHistoryRows = %+v, want 2 of each", got)
	}

	events, err := d.AvailabilityEvents(0)
	if err != nil {
		t.Fatalf("AvailabilityEvents: %v", err)
	}
	if int64(len(events)) != got.Events {
		t.Errorf("the count says %d events and the read returned %d", got.Events, len(events))
	}
}
