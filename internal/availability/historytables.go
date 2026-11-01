package availability

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The row half of the two histories: each observation is one row of
// availability_event or latency_sample, and the kv documents are gone. A row
// carries this machine's own installation id, a fresh ULID and its time as
// Unix milliseconds. The rows are append-only, so the insert trigger beside
// them is the only trigger they have: the prune below deletes without
// recording, and nothing is ever updated.

// recordEvent commits one availability observation as one row of
// availability_event. A failure is printed and dropped, the same rule
// AppendEntryLocked applies to the ledger mirror: the ledger write is the one
// that matters and it has already happened. A Deps with no shared file -- a
// test, or a runtime built with no database open -- records nothing at all,
// which is the machine-with-no-history reading rather than a second record.
func recordEvent(d Deps, ev Event) {
	if err := insertEventRow(d, ev); err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record history: %v\n", err)
	}
}

// insertEventRow is one INSERT of one observation into availability_event. A
// row that failed to insert must not fail the ledger write.
func insertEventRow(d Deps, ev Event) error {
	if d.History == nil {
		return nil
	}
	row := db.AvailabilityEvent{
		ID:       db.NewID(),
		AtMS:     db.UnixMS(ev.At),
		Kind:     string(ev.Kind),
		Provider: ev.Provider,
		Account:  ev.Account,
		Token:    ev.Token,
		Source:   ev.Source,
		Binding:  ev.Binding,
		Note:     ev.Note,
	}
	if !ev.Since.IsZero() {
		since := db.UnixMS(ev.Since)
		row.SinceMS = &since
	}
	return d.History.InsertAvailabilityEvent(d.History.Origin(), row)
}

// recordSample commits one probe result as one row of latency_sample, under the
// same rule recordEvent commits an observation: a failure is returned rather
// than failed, because a probe that measured a candidate must not be reported as
// broken because its history could not be written.
func recordSample(d Deps, s Sample) error {
	return insertSampleRow(d, s)
}

func insertSampleRow(d Deps, s Sample) error {
	if d.History == nil {
		return nil
	}
	return d.History.InsertLatencySample(d.History.Origin(), db.LatencySample{
		ID:      db.NewID(),
		AtMS:    db.UnixMS(s.At),
		Token:   s.Token,
		Host:    s.Host,
		TTFTMS:  s.TTFTMS,
		TotalMS: s.TotalMS,
		Err:     s.Err,
	})
}

// PruneHistory removes every availability event and latency sample older than
// the retention window, at d's clock, for every origin. It is the daemon's
// maintenance step rather than a per-write one, so a machine with no daemon
// behind it still keeps its rows; the read-time Prune on each of the two
// documents is what covers it.
//
// The rows it removes are not recorded anywhere: the tables carry an insert
// trigger alone, so an age delete exports nothing and no importer has to
// reproduce it. Every machine prunes on its own clock, which is why the window is
// a window rather than a set.
func PruneHistory(d Deps) (db.HistoryPruned, error) {
	if d.History == nil {
		return db.HistoryPruned{}, nil
	}
	cutoff := db.UnixMS(d.Now().Add(-HistoryRetainWindow))
	return d.History.PruneHistory(cutoff)
}
