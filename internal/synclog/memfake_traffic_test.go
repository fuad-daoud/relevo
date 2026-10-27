package synclog

// The traffic rows on the shared fake log: one row per origin, each handle
// moving only its own, and every publish answering with the whole set.

import (
	"testing"
	"time"
)

// trafficAt is when a publish says it read its counters, pinned so the rows
// a case compares are about the write rather than about a clock.
var trafficAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// TestMemTransportsSeeEachOtherTrafficRows pins the two rules the fake log
// carries for traffic: a publish answers with every origin's row as the log
// holds it after the write, and a handle can move only its own origin's row.
// The second is the rule PublishMarks pins for marks, and two handles on one
// log are the only way a single-process test can exercise it.
func TestMemTransportsSeeEachOtherTrafficRows(t *testing.T) {
	a := NewMemTransport("origin-a")
	b := a.OnLog("origin-b")

	rows, err := a.PublishTraffic(TrafficRow{Month: "2026-10", Push: 10, Pull: 20, At: trafficAt})
	if err != nil {
		t.Fatalf("publish traffic: %v", err)
	}
	if len(rows) != 1 || rows["origin-a"].Push != 10 {
		t.Fatalf("the first publish answered with %+v, want only origin-a's row", rows)
	}

	got, err := b.PublishTraffic(TrafficRow{Month: "2026-10", Push: 1, Pull: 2, At: trafficAt})
	if err != nil {
		t.Fatalf("publish from the second handle: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("b sees %d rows, want two: %+v", len(got), got)
	}
	if got["origin-a"] != (TrafficRow{Month: "2026-10", Push: 10, Pull: 20, At: trafficAt}) {
		t.Errorf("a's row after b's publish = %+v, want it untouched", got["origin-a"])
	}

	after, err := a.PublishTraffic(TrafficRow{Month: "2026-11", Push: 30, Pull: 40, At: trafficAt})
	if err != nil {
		t.Fatalf("republish from the first handle: %v", err)
	}
	if after["origin-a"].Month != "2026-11" || after["origin-a"].Push != 30 {
		t.Errorf("a's row after its republish = %+v, want its own row overwritten", after["origin-a"])
	}
	if after["origin-b"].Push != 1 {
		t.Errorf("b's row after a's republish = %+v, want it untouched: a handle writes only its own",
			after["origin-b"])
	}
}
