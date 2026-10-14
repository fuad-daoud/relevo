package db

import (
	"fmt"
	"time"
)

// The write and retention seam of the two history tables. Both are append-only:
// a row is never updated and is deleted only by age, so the whole seam is one
// insert, one age delete and the two conversions the at_ms column needs.
//
// The insert is a plain INSERT rather than the exchange upsert, because a row
// here is written once and never rewritten. The insert outbox trigger beside it
// records the write, so a row lands in the log the same way every other shared
// row does; the age delete has no trigger, so it lands in nothing, which is what
// keeps a row every machine prunes on its own clock from being carried around as
// a delete every other machine would then have to reproduce.

// HistoryWindow is how long a row of the availability or latency history is
// kept. It lives here rather than in the package that renders the window because
// three packages have to agree on it -- the writers that stamp a row, the
// importer that skips a row already older than it, and the reconciler that
// refuses to propose one -- and this is the only one of the three they all
// import.
const HistoryWindow = 30 * 24 * time.Hour

// The names the history tables' rows are keyed and aged by. They are named here
// so a caller passes a column and not a literal, and so a migration that renamed
// one failed to compile rather than to compare the wrong column.
const (
	historyAtColumn = "at_ms"
	// AvailabilityEventTable and LatencySampleTable are the two append-only
	// shared tables, named for the callers that write or prune them.
	AvailabilityEventTable = "availability_event"
	LatencySampleTable     = "latency_sample"
)

// UnixMS is the integer a history row's At is stored as: Unix milliseconds in
// UTC. The comparison the age prune and the arrival window make is a numeric one
// for exactly the reason it is not text -- today's events carry mixed offsets
// and variable-width fractional seconds, so '2026-10-14T09:00:00Z' sorts after
// '2026-10-14T10:00:00+03:00'.
func UnixMS(t time.Time) int64 { return t.UTC().UnixMilli() }

// TimeFromUnixMS is UnixMS read back into a time.Time. The zero millisecond is
// the zero time rather than 1970, so a row without a time reads as a row with no
// time at all.
func TimeFromUnixMS(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// AvailabilityEvent is one row of availability_event: a ledger observation
// mirrored into the window the ledger itself does not keep. SinceMS is the at of
// the oldest ledger entry a clear removed, and nil on every other kind, which is
// how a cleared event that cleared nothing reads.
type AvailabilityEvent struct {
	ID       string
	AtMS     int64
	Kind     string
	Provider string
	Account  string
	Token    string
	Source   string
	Binding  string
	Note     string
	SinceMS  *int64
}

// LatencySample is one row of latency_sample: one probe of one candidate.
type LatencySample struct {
	ID      string
	AtMS    int64
	Token   string
	Host    string
	TTFTMS  int64
	TotalMS int64
	Err     string
}

// InsertAvailabilityEvent writes one availability observation as its own row,
// stamped under origin. The outbox trigger on the table records it.
func (d *DB) InsertAvailabilityEvent(origin string, e AvailabilityEvent) error {
	return d.Tx(func(tx *Tx) error { return tx.insertAvailabilityEvent(origin, e) })
}

func (t *Tx) insertAvailabilityEvent(origin string, e AvailabilityEvent) error {
	const query = `INSERT INTO availability_event
		(id, at_ms, kind, provider, account, token, source, binding, note, since_ms, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := t.exec(query, e.ID, e.AtMS, e.Kind, e.Provider, e.Account, e.Token,
		e.Source, e.Binding, e.Note, e.SinceMS, origin); err != nil {
		return fmt.Errorf("db: insert availability_event %s: %w", e.ID, err)
	}
	return nil
}

// InsertLatencySample writes one probe result as its own row, stamped under
// origin. The outbox trigger on the table records it.
func (d *DB) InsertLatencySample(origin string, s LatencySample) error {
	return d.Tx(func(tx *Tx) error { return tx.insertLatencySample(origin, s) })
}

func (t *Tx) insertLatencySample(origin string, s LatencySample) error {
	const query = `INSERT INTO latency_sample
		(id, at_ms, token, host, ttft_ms, total_ms, err, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := t.exec(query, s.ID, s.AtMS, s.Token, s.Host, s.TTFTMS, s.TotalMS, s.Err, origin); err != nil {
		return fmt.Errorf("db: insert latency_sample %s: %w", s.ID, err)
	}
	return nil
}

// HistoryPruned is how many rows a retention pass removed from each table.
type HistoryPruned struct {
	Events  int64
	Samples int64
}

// PruneHistory deletes every row of both history tables older than cutoffMS, for
// every origin rather than this machine's own: a machine holds a peer's history
// as soon as it imports it, and what it shows is a window, so a peer's row ages
// out here on the same clock as this machine's.
//
// The delete is not transactional with anything else and does not record an
// outbox entry, which is why it may run on a maintenance tick without a sync
// behind it. Both statements run in one transaction so a machine is never left
// with one table pruned and the other not.
func (d *DB) PruneHistory(cutoffMS int64) (HistoryPruned, error) {
	var out HistoryPruned
	err := d.Tx(func(tx *Tx) error {
		var err error
		if out.Events, err = tx.pruneHistoryTable(AvailabilityEventTable, cutoffMS); err != nil {
			return err
		}
		out.Samples, err = tx.pruneHistoryTable(LatencySampleTable, cutoffMS)
		return err
	})
	if err != nil {
		return HistoryPruned{}, err
	}
	return out, nil
}

func (t *Tx) pruneHistoryTable(tbl string, cutoffMS int64) (int64, error) {
	query := "DELETE FROM " + quoteIdent(tbl) + " WHERE " + quoteIdent(historyAtColumn) + " < ?"
	res, err := t.exec(query, cutoffMS)
	if err != nil {
		return 0, fmt.Errorf("db: prune %s: %w", tbl, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: prune %s: count: %w", tbl, err)
	}
	return n, nil
}

// HistoryAt reads a history row's at_ms out of the values a body carried, and
// whether the body named it at all. A body that names no time is not an aged
// row: the arrival window cannot judge a row whose time it does not have, so the
// row is judged on the column's own NOT NULL at write time instead.
func HistoryAt(tbl string, values map[string]any) (int64, bool) {
	if !AppendOnly(tbl) {
		return 0, false
	}
	switch at := values[historyAtColumn].(type) {
	case int64:
		return at, true
	case float64:
		return int64(at), true
	case int:
		return int64(at), true
	default:
		return 0, false
	}
}

// HistoryAtMS is one row's at_ms as the shared walk read it, and whether the row
// carried one. A walk page names the columns by their own names, so the value is
// found the same way a reader would find it.
func HistoryAtMS(row ExchangeRow) (int64, bool) {
	for _, col := range row.Columns {
		if col.Name != historyAtColumn {
			continue
		}
		switch at := col.Value.(type) {
		case int64:
			return at, true
		case float64:
			return int64(at), true
		default:
			return 0, false
		}
	}
	return 0, false
}
