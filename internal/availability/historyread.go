package availability

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The read seam of the two histories: what LoadHistory and LoadLatency read
// from the history tables.
//
// A read combines every origin's rows inside the retention window, because the
// file holds a peer's history as soon as it imports it and what a display shows
// is the window across the fleet. It returns the unchanged History and
// LatencyHistory shapes, so HourCounts, BlockedDurations, Summary, StatsInputs,
// formatHistory and peakText need no signature change: they see the same values
// assembled from rows.
//
// What a read must never become is an input to a decision. Nothing here reaches
// a pick: resolveCandidate and resolveRole read availability.Gates, which is
// the live ledger, and the gates document carries a rate limit to every machine
// because a provider's quota is the provider's. A peer's outages and latencies
// are a display concern and not a scheduling one.

// LoadHistory reads the availability history for display, pruning it to the
// retention window at d's clock. Every origin's rows inside the window are
// combined and ordered oldest first. A Deps with no shared file -- a test, or a
// runtime built with no database open -- reads as empty; a read that fails is
// an error the caller reports, the same rule Gates applies to the ledger.
func LoadHistory(d Deps) (History, error) {
	if d.History == nil {
		return History{}, nil
	}
	rows, err := d.History.AvailabilityEvents(historyCutoffMS(d.Now()))
	if err != nil {
		return History{}, fmt.Errorf("read availability history: %w", err)
	}
	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, eventOf(row))
	}
	return History{Events: events}.Prune(d.Now()), nil
}

// LoadLatency reads the latency history for display: every origin's samples
// inside the retention window, oldest first, under the same rule LoadHistory
// reads events by.
func LoadLatency(d Deps) (LatencyHistory, error) {
	if d.History == nil {
		return LatencyHistory{}, nil
	}
	rows, err := d.History.LatencySamples(historyCutoffMS(d.Now()))
	if err != nil {
		return LatencyHistory{}, fmt.Errorf("read latency history: %w", err)
	}
	samples := make([]Sample, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, Sample{
			At:      db.TimeFromUnixMS(row.AtMS),
			Token:   row.Token,
			Host:    row.Host,
			TTFTMS:  row.TTFTMS,
			TotalMS: row.TotalMS,
			Err:     row.Err,
		})
	}
	return LatencyHistory{Samples: samples}.Prune(d.Now()), nil
}

// historyCutoffMS is the oldest row the retention window admits, as the integer
// the tables order and compare by. It is the same conversion the age prune and
// the importer's arrival window make, so a reader, the prune and the importer
// cannot disagree about where the window ends.
func historyCutoffMS(now time.Time) int64 {
	return db.UnixMS(now.Add(-HistoryRetainWindow))
}

// eventOf is one availability_event row as the Event every reader already
// takes. A nil since_ms reads as a zero Since, which is what it means on every
// kind but a clear.
func eventOf(row db.AvailabilityEvent) Event {
	ev := Event{
		At:       db.TimeFromUnixMS(row.AtMS),
		Kind:     Kind(row.Kind),
		Provider: row.Provider,
		Account:  row.Account,
		Token:    row.Token,
		Source:   row.Source,
		Binding:  row.Binding,
		Note:     row.Note,
	}
	if row.SinceMS != nil {
		ev.Since = db.TimeFromUnixMS(*row.SinceMS)
	}
	return ev
}
