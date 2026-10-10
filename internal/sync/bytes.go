package sync

// The bytes this machine has moved this month, kept in the machine-local file so
// a quota is read against what this installation spent. The counts are exact:
// they come from the worker's own totals, not from sizes estimated at the edges.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
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

func counterDelta(prev, cur int64) int64 {
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
// row. It runs after every attempt, failed or not: a push that moved bytes before
// it failed still spent them. A reading that cannot be taken is skipped, because
// the next attempt's reading carries the same totals.
func (r *Runner) countBytes(ctx context.Context, t *StopTransport) {
	var cur synclog.Stats
	if err := within(ctx, t, r.stepTimeout(), func() error {
		var err error
		cur, err = t.Stats()
		return err
	}); err != nil {
		return
	}
	r.readMu.Lock()
	defer r.readMu.Unlock()
	delta := bytesSince(r.lastRead, cur)
	r.lastRead = cur
	if delta.Zero() {
		return
	}
	if err := addBytes(r.Local, time.Now(), delta); err != nil {
		slog.Warn("sync: count the bytes moved", "err", err)
	}
}
