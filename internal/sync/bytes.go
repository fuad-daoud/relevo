package sync

// The bytes this machine has moved this month, kept in the machine-local file so
// a quota is read against what this installation spent. The counts are exact:
// they come from the worker's own totals, not from sizes estimated at the edges.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ByteCounters is one month's traffic: what went to and came from Turso, and
// what went to and came from the bucket.
type ByteCounters struct {
	TursoPush int64 `json:"turso_push"`
	TursoPull int64 `json:"turso_pull"`
	R2Put     int64 `json:"r2_put"`
	R2Get     int64 `json:"r2_get"`
}

// BytesKey is the kv row the month holding t is counted in. UTC, so two
// machines in different zones agree on which month a byte belongs to.
func BytesKey(t time.Time) string { return "sync.bytes." + t.UTC().Format("2006-01") }

// Zero reports whether nothing moved.
func (c ByteCounters) Zero() bool { return c == ByteCounters{} }

// ReadBytes returns the month holding t. A month nothing was counted in reads as
// zero rather than as an error.
func ReadBytes(kv db.KV, t time.Time) (ByteCounters, error) {
	var c ByteCounters
	if err := marker(kv, BytesKey(t), &c); err != nil {
		return ByteCounters{}, err
	}
	return c, nil
}

// addBytes adds d to the month holding t.
func addBytes(kv db.KV, t time.Time, d ByteCounters) error {
	c, err := ReadBytes(kv, t)
	if err != nil {
		return err
	}
	c.TursoPush += d.TursoPush
	c.TursoPull += d.TursoPull
	c.R2Put += d.R2Put
	c.R2Get += d.R2Get
	body, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("sync: encode the byte counters: %w", err)
	}
	if err := kv.KVPut(BytesKey(t), body); err != nil {
		return fmt.Errorf("sync: write the byte counters: %w", err)
	}
	return nil
}

// bytesSince is what the worker moved between two readings of its cumulative
// totals. A total lower than the last reading means the worker restarted and
// counts from zero again, so the new total is itself the delta; subtracting
// would be negative and would lose everything the new worker moved.
func bytesSince(prev, cur synclog.Stats) ByteCounters {
	return ByteCounters{
		TursoPush: counterDelta(prev.TursoSent, cur.TursoSent),
		TursoPull: counterDelta(prev.TursoReceived, cur.TursoReceived),
		R2Put:     counterDelta(prev.R2Put, cur.R2Put),
		R2Get:     counterDelta(prev.R2Get, cur.R2Get),
	}
}

// Transfers is what one attempt moved over the wire: how many engine pushes
// and pulls ran and the Turso bytes they carried. It is the delta of two
// cumulative readings, so an attempt that pulled once and moved megabytes
// reads differently from one that pulled a hundred times for kilobytes.
type Transfers struct {
	Pulls     int   `json:"pulls"`
	Pushes    int   `json:"pushes"`
	TursoPush int64 `json:"turso_push"`
	TursoPull int64 `json:"turso_pull"`
}

// transfersSince is the attempt's share of two readings: the same restart rule
// as the byte totals, applied to the transfer counts beside them.
func transfersSince(prev, cur synclog.Stats) Transfers {
	return Transfers{
		Pulls:     countDelta(prev.TursoPulls, cur.TursoPulls),
		Pushes:    countDelta(prev.TursoPushes, cur.TursoPushes),
		TursoPush: counterDelta(prev.TursoSent, cur.TursoSent),
		TursoPull: counterDelta(prev.TursoReceived, cur.TursoReceived),
	}
}

func counterDelta(prev, cur int64) int64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

func countDelta(prev, cur int) int {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// FormatBytes is the status clause for a month's counters: the four counts, and
// the Turso sync total as a share of its quota.
func FormatBytes(c ByteCounters, quotaTursoSync int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "this month: turso push %s, pull %s; r2 put %s, get %s",
		humanBytes(c.TursoPush), humanBytes(c.TursoPull), humanBytes(c.R2Put), humanBytes(c.R2Get))
	if quotaTursoSync > 0 {
		total := c.TursoPush + c.TursoPull
		fmt.Fprintf(&b, "; turso sync %s of %s (%.1f%%)",
			humanBytes(total), humanBytes(quotaTursoSync), float64(total)*100/float64(quotaTursoSync))
	}
	return b.String()
}

// humanBytes renders a count in powers of 1000, which is how the providers bill.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// countBytes folds the worker's totals since the last reading into this month's
// row, and reports the attempt's share measured from base: the totals as the
// attempt found them. It runs after every attempt, failed or not: a push that
// moved bytes before it failed still spent them. A reading that cannot be taken
// is skipped, because the next attempt's reading carries the same totals.
func (r *Runner) countBytes(ctx context.Context, t *StopTransport, base synclog.Stats) (Transfers, bool) {
	var cur synclog.Stats
	if err := within(ctx, t, r.stepTimeout(), func() error {
		var err error
		cur, err = t.Stats()
		return err
	}); err != nil {
		return Transfers{}, false
	}
	r.readMu.Lock()
	defer r.readMu.Unlock()
	recordStats(r.Local, &r.lastRead, cur)
	return transfersSince(base, cur), true
}

// snapshotTotals reads the worker's cumulative totals as the attempt found
// them, without asking the worker: the last reading is what the next one is
// measured against, so it is the attempt's baseline. No extra engine call is
// spent on it; the attempt's own transfers are what move the totals between
// the two readings.
func (r *Runner) snapshotTotals() synclog.Stats {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	return r.lastRead
}

// CountMoved folds what a one-shot verb's transport moved into this month's row
// through the same baseline the steady loop reads, so the next attempt measures
// from here and recounts nothing. It is meant to be deferred: the bytes a failed
// verb moved were spent too. The verb's own transfers are not reported: they
// belong to no attempt.
func (r *Runner) CountMoved(ctx context.Context, t *StopTransport) {
	r.countBytes(ctx, t, r.snapshotTotals())
}

// SeedStats sets the reading the next count is measured against, for a worker a
// join already counted up to cur.
func (r *Runner) SeedStats(cur synclog.Stats) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	r.lastRead = cur
}

// recordStats folds the worker's totals since *last into this month's row and
// moves *last to cur. The steady loop and the join both count through it, so a
// reset worker is handled by one rule.
func recordStats(kv db.KV, last *synclog.Stats, cur synclog.Stats) {
	delta := bytesSince(*last, cur)
	*last = cur
	if delta.Zero() {
		return
	}
	if err := addBytes(kv, time.Now(), delta); err != nil {
		slog.Warn("sync: count the bytes moved", "err", err)
	}
}

// TablesKey is the kv row the month holding t is broken down in: one row per
// table beside the byte counters' row. UTC, like the counters.
func TablesKey(t time.Time) string { return "sync.tables." + t.UTC().Format("2006-01") }

// ReadTables returns the month holding t's per-table breakdown. A month nothing
// was tallied in reads as nil rather than as an error.
func ReadTables(kv db.KV, t time.Time) (synclog.TableTallies, error) {
	var out synclog.TableTallies
	if err := marker(kv, TablesKey(t), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// countTables folds one attempt's per-table breakdown into this month's row.
// An attempt that moved no rows tallies nothing and writes nothing. The map is
// bounded by the schema's table names, so unlike the attempt ring it needs no
// cap: no attempt can invent a table.
func (r *Runner) countTables(tables synclog.TableTallies) {
	if len(tables) == 0 {
		return
	}
	month, err := ReadTables(r.Local, time.Now())
	if err != nil {
		slog.Warn("sync: read the table breakdown", "err", err)
		return
	}
	month.Add(tables)
	body, err := json.Marshal(month)
	if err != nil {
		slog.Warn("sync: encode the table breakdown", "err", err)
		return
	}
	if err := r.Local.KVPut(TablesKey(time.Now()), body); err != nil {
		slog.Warn("sync: write the table breakdown", "err", err)
	}
}

// maxTablesLine caps the tables on the status line. The document carries every
// table; the line names the heaviest few, so one busy month does not push the
// rest of the line off the screen.
const maxTablesLine = 3

// FormatTables is the status clause for the month's per-table breakdown: the
// tables whose entries weighed most, with their entry counts and body bytes.
// Bytes order the tables because the question the breakdown answers is which
// table dominates the wire; the name breaks ties so the line is stable.
func FormatTables(tables synclog.TableTallies) string {
	type named struct {
		name    string
		entries int
		bytes   int64
	}
	all := make([]named, 0, len(tables))
	for name, tally := range tables {
		all = append(all, named{
			name:    name,
			entries: tally.ExportedEntries + tally.ImportedEntries,
			bytes:   tally.ExportedBytes + tally.ImportedBytes,
		})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].bytes != all[j].bytes {
			return all[i].bytes > all[j].bytes
		}
		return all[i].name < all[j].name
	})
	shown := all
	more := ""
	if len(all) > maxTablesLine {
		shown = all[:maxTablesLine]
		more = fmt.Sprintf(", +%d more", len(all)-maxTablesLine)
	}
	parts := make([]string, 0, len(shown))
	for _, n := range shown {
		// The name arrived over the log, so it is rendered sanitized like
		// every other wire text on this line; the document keeps it raw.
		parts = append(parts, fmt.Sprintf("%s %d rows %s", sanitize.Text(n.name), n.entries, humanBytes(n.bytes)))
	}
	return "tables: " + strings.Join(parts, ", ") + more
}
