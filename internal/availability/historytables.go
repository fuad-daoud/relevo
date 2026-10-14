package availability

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The row half of the two histories. Each observation is committed twice here:
// once into the kv document the reads still use, and once into its own row of
// availability_event or latency_sample. The row is what a peer can hold and what
// the age prune deletes; the document is what today's readers read, so both are
// written and neither replaces the other yet.
//
// A row is written with this machine's own installation id and a fresh ULID, and
// with its time as Unix milliseconds rather than as RFC3339 text. The rows are
// append-only, so the insert trigger beside them is the only trigger they have:
// the prune below deletes without recording, and nothing is ever updated.

// recordEvent commits one availability observation to the kv document and to one
// row of availability_event. A failure of either is printed and dropped, the
// same rule AppendEntryLocked applies to the ledger mirror: the ledger write is
// the one that matters and it has already happened. A Deps with no history
// handle -- a test, or a runtime built before the rows existed -- records the
// document alone.
func recordEvent(d Deps, ev Event) {
	err := saveEventDoc(d, ev)
	if err == nil {
		err = insertEventRow(d, ev)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record history: %v\n", err)
	}
}

// insertEventRow is one INSERT of one observation into availability_event. It is
// a separate step from the document write rather than part of it: the two live in
// different tables of the same file, and a row that failed to insert must not
// stop the document from being written.
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

// saveEventDoc is the kv document write: the whole window pruned to the
// retention window with the new event appended, exactly as it was written before
// the rows existed.
func saveEventDoc(d Deps, ev Event) error {
	h, err := loadHistory(d.Gates)
	if err != nil {
		return err
	}
	return SaveHistory(d.Gates, h.Prune(d.Now()).Append(ev))
}

// recordSample commits one probe result to the kv document and to one row of
// latency_sample, under the same rules recordEvent commits an observation: the
// document first, the row beside it, and a failure of either is printed rather
// than failed, because a probe that measured a candidate must not be reported as
// broken because its history could not be written.
func recordSample(d Deps, s Sample) error {
	err := saveSampleDoc(d, s)
	if err == nil {
		err = insertSampleRow(d, s)
	}
	return err
}

// insertSampleRow is one INSERT of one probe result into latency_sample.
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

// saveSampleDoc is the kv document write for one probe result.
func saveSampleDoc(d Deps, s Sample) error {
	h, err := LoadLatency(d.Latency)
	if err != nil {
		return err
	}
	return SaveLatency(d.Latency, h.Prune(d.Now()).Append(s))
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
