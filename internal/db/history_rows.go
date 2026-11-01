package db

import (
	"fmt"
	"math"
	"time"
)

// The write and retention seam of the two history tables. Both are append-only:
// a row is never updated and is deleted only by age.
//
// The insert is a plain INSERT rather than the exchange upsert, because a row
// here is written once and never rewritten. The outbox trigger beside the insert
// records the write; the age delete has no trigger, so a row every machine
// prunes on its own clock is never carried around as a delete every other
// machine would then have to reproduce.
//
// Plain INSERT does not tolerate a second write of an id already present, and
// the one-time migration needs that: it derives its ids from the documents it
// reads, so a retry writes the rows it already wrote. It gets its own entry
// points with OR IGNORE, rather than the live writers being relaxed for it.

// HistoryWindow is how long a row of the availability or latency history is
// kept. It lives here because three packages have to agree on it -- the writers
// that stamp a row, the importer that skips a row already older than it, and the
// reconciler that refuses to propose one -- and this is the only one they all
// import.
const HistoryWindow = 30 * 24 * time.Hour

// HistoryClockSkew is how far ahead of this machine's clock an arriving history
// row's at_ms may sit and still be written: a peer whose clock runs a little
// fast stamps rows this machine would otherwise refuse, and refusing those
// throws away real observations over nothing but a clock.
//
// It is minutes on purpose. The allowance covers drift between two machines
// reading the time, not a body naming a time of its own choosing: a row a day
// or a year ahead ages out of no window, so writing one pins a row in every
// machine's table that no prune can ever reach.
const HistoryClockSkew = 5 * time.Minute

// HistoryFutureMS is the latest at_ms an arriving history row may carry and
// still be written: now plus the clock-skew allowance, stated here so the bound
// and the window it guards sit next to each other.
func HistoryFutureMS(now time.Time) int64 {
	return UnixMS(now.Add(HistoryClockSkew))
}

// The columns the history tables' rows are keyed and aged by, named here so a
// caller passes a column and not a literal, and so renaming one fails to
// compile rather than comparing the wrong column.
const (
	historyAtColumn = "at_ms"
	// AvailabilityEventTable and LatencySampleTable are the two append-only
	// shared tables.
	AvailabilityEventTable = "availability_event"
	LatencySampleTable     = "latency_sample"
)

// UnixMS is the integer a history row's At is stored as: Unix milliseconds in
// UTC.
func UnixMS(t time.Time) int64 { return t.UTC().UnixMilli() }

// TimeFromUnixMS is UnixMS read back into a time.Time: the zero millisecond is
// the zero time, so a row without a time reads as a row with no time at all.
func TimeFromUnixMS(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// AvailabilityEvent is one row of availability_event. SinceMS is the at of the
// oldest ledger entry a clear removed, and nil on every other kind, which is how
// a cleared event that cleared nothing reads.
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

// historyConflict is the clause that decides what a second write of an id
// already in the table does: historyStrict reports it as the caller's bug, and
// historyMigrated leaves the row alone so a retried pass finds the rows it
// already wrote.
//
// The value is concatenated into the statement, so it is a constant of this
// file and never a caller's argument.
type historyConflict string

const (
	historyStrict   historyConflict = "INSERT"
	historyMigrated historyConflict = "INSERT OR IGNORE"
)

// InsertAvailabilityEvent writes one availability observation stamped under
// origin. The outbox trigger on the table records it, and a second write of the
// same id is refused.
func (d *DB) InsertAvailabilityEvent(origin string, e AvailabilityEvent) error {
	return d.Tx(func(tx *Tx) error { return tx.insertAvailabilityEvent(origin, e) })
}

func (t *Tx) insertAvailabilityEvent(origin string, e AvailabilityEvent) error {
	return t.insertAvailabilityEventConflict(origin, e, historyStrict)
}

// InsertMigratedAvailabilityEvent writes one availability observation and
// tolerates the row already being there: a retried pass offers the ids the
// crashed pass already wrote, and OR IGNORE turns that collision into "leave the
// row alone" instead of a failed retry. An ignored duplicate inserts no row, so
// the retry records nothing a peer would then apply twice.
func (d *DB) InsertMigratedAvailabilityEvent(origin string, e AvailabilityEvent) error {
	return d.Tx(func(tx *Tx) error {
		return tx.insertAvailabilityEventConflict(origin, e, historyMigrated)
	})
}

func (t *Tx) insertAvailabilityEventConflict(origin string, e AvailabilityEvent, on historyConflict) error {
	query := string(on) + ` INTO availability_event
		(id, at_ms, kind, provider, account, token, source, binding, note, since_ms, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := t.exec(query, e.ID, e.AtMS, e.Kind, e.Provider, e.Account, e.Token,
		e.Source, e.Binding, e.Note, e.SinceMS, origin); err != nil {
		return fmt.Errorf("db: insert availability_event %s: %w", e.ID, err)
	}
	return nil
}

// InsertLatencySample writes one probe result stamped under origin. The outbox
// trigger on the table records it, and a second write of the same id is refused.
func (d *DB) InsertLatencySample(origin string, s LatencySample) error {
	return d.Tx(func(tx *Tx) error { return tx.insertLatencySample(origin, s) })
}

func (t *Tx) insertLatencySample(origin string, s LatencySample) error {
	return t.insertLatencySampleConflict(origin, s, historyStrict)
}

// InsertMigratedLatencySample writes one probe result as its own row and
// tolerates the row already being there, under the same rule
// InsertMigratedAvailabilityEvent writes an event twice.
func (d *DB) InsertMigratedLatencySample(origin string, s LatencySample) error {
	return d.Tx(func(tx *Tx) error {
		return tx.insertLatencySampleConflict(origin, s, historyMigrated)
	})
}

func (t *Tx) insertLatencySampleConflict(origin string, s LatencySample, on historyConflict) error {
	query := string(on) + ` INTO latency_sample
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

// PruneHistory deletes every row of both history tables older than cutoffMS,
// for every origin rather than this machine's own: a machine holds a peer's
// history as soon as it imports it, so a peer's row ages out here on the same
// clock as this machine's.
//
// The delete records no outbox entry, which is why it may run on a maintenance
// tick without a sync behind it. Both statements run in one transaction so a
// machine is never left with one table pruned and the other not.
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
// whether the body named a time this package can read.
//
// Only a whole int64, int or integral float64 is a time here: the column is an
// integer and the age prune compares it as one. Anything else is reported as
// unreadable rather than as a time of zero, because a caller that cannot read
// the time must not judge the row, and guessing writes a row that no prune can
// reach.
func HistoryAt(tbl string, values map[string]any) (int64, bool) {
	if !AppendOnly(tbl) {
		return 0, false
	}
	switch at := values[historyAtColumn].(type) {
	case int64:
		return at, true
	case int:
		return int64(at), true
	case float64:
		if !wholeNumber(at) {
			return 0, false
		}
		return int64(at), true
	default:
		return 0, false
	}
}

// wholeNumber reports whether f is a value the integer column can hold whole: a
// real with no fraction, and inside the range an int64 covers. A real beyond it
// would wrap to a time this machine treats as long past.
func wholeNumber(f float64) bool {
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return false
	}
	return f >= math.MinInt64 && f < math.MaxInt64
}

// HistoryAtMS is one row's at_ms as the shared walk read it, and whether the
// row carried one.
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
