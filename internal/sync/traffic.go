package sync

// Each machine's month of Turso traffic, published into the shared log so a
// quota can be read against the account rather than against the one machine
// answering. The local snapshot is written only by a successful publish, so a
// machine that cannot reach the log keeps the last account it saw rather than
// losing the total to the failed read.

import (
	"errors"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

const (
	// KeyTrafficPublished is when this machine last published its traffic row.
	// It is a marker for the reason the marks marker is: the throttle is what
	// the next attempt reads, and a daemon that has been running since before
	// the row existed has no field to carry it.
	KeyTrafficPublished = "sync.traffic.published"
	// KeyAccount is the local snapshot of every machine's published row, read
	// from the log at the last successful publish.
	KeyAccount = "sync.account"
)

// trafficEvery is the shortest gap between two traffic publishes. A publish is
// a Turso write and a push, and the total it feeds is read against a monthly
// quota, so an hour-old row loses nothing a transfer on every tick would buy.
const trafficEvery = time.Hour

// AccountSnapshot is what a publish read back from the log: every origin's
// row, and when the rows were read.
type AccountSnapshot struct {
	ReadAt time.Time                     `json:"read_at"`
	Rows   map[string]synclog.TrafficRow `json:"rows"`
}

// Account is one month's account-wide traffic as the snapshot and this
// machine's live counters together say it.
type Account struct {
	// Month is the UTC month the total covers, spelled "2006-01".
	Month string
	// Total is the Turso bytes every counted machine pushed and pulled.
	Total int64
	// Machines is how many machines the total counts.
	Machines int
	// ReadAt is when the snapshot was taken.
	ReadAt time.Time
}

// ReadAccount reads the local snapshot of every machine's published row. A
// machine that has never completed a publish reads the zero snapshot rather
// than an error, the way ReadBytes reads an absent month.
func ReadAccount(kv db.KV) (AccountSnapshot, error) {
	var s AccountSnapshot
	if err := marker(kv, KeyAccount, &s); err != nil {
		return AccountSnapshot{}, err
	}
	return s, nil
}

// AccountTraffic folds the snapshot and this machine's live counters into one
// account total for the month holding now.
//
// This machine's share comes from the counters the caller passes rather than
// its own snapshot row, because the live counters are fresher than anything
// the log could answer with. Another origin's row counts only when it names
// the same month: a row from an earlier month was billed in that month, and
// counting it again would charge this month for bytes the quota has already
// carried. A previous month's row left un-overwritten -- a machine that has
// gone silent -- is therefore excluded rather than guessed at.
func AccountTraffic(snap AccountSnapshot, own ByteCounters, origin string, now time.Time) Account {
	month := now.UTC().Format("2006-01")
	a := Account{Month: month, Total: own.TursoPush + own.TursoPull, Machines: 1, ReadAt: snap.ReadAt}
	for other, row := range snap.Rows {
		if other == origin || row.Month != month {
			continue
		}
		a.Total += row.Push + row.Pull
		a.Machines++
	}
	return a
}

// trafficDue reports whether a traffic publish is due: one is due when the
// marker is absent, which is a machine that has never published, and again
// once trafficEvery has passed since the last.
//
// A marker that will not parse reads as due rather than as never due, the
// rule marksDue runs on for the reason it gives: the worst a bad marker costs
// is a publish the throttle was going to skip.
func (r *Runner) trafficDue(now time.Time) bool {
	var last time.Time
	if err := marker(r.Local, KeyTrafficPublished, &last); err != nil {
		slog.Warn("sync: read the traffic marker", "err", err)
		return true
	}
	return last.IsZero() || now.Sub(last) >= trafficEvery
}

// publishTraffic hands the log this machine's month of Turso bytes and keeps a
// snapshot of every machine's row in answer.
//
// It runs on every steady attempt whether or not the import moved a mark,
// which publishMarks requires: the account total goes most wrong on a machine
// that exports heavily with no import progress, and gating on import progress
// would never publish its traffic.
//
// A publish that fails is logged rather than folded into the attempt's result,
// the rule publishMarks runs on: the exchange itself succeeded, and the marker
// and the snapshot move only after a success, so the next attempt retries.
func (r *Runner) publishTraffic(t *StopTransport, now time.Time) {
	if r.Local == nil || !r.trafficDue(now) {
		return
	}
	counters, err := ReadBytes(r.Local, now)
	if err != nil {
		slog.Warn("sync: read this month's byte counters", "err", err)
		return
	}
	rows, err := t.PublishTraffic(synclog.TrafficRow{
		Month: now.UTC().Format("2006-01"),
		Push:  counters.TursoPush,
		Pull:  counters.TursoPull,
		At:    now,
	})
	if err != nil {
		if errors.Is(err, errTrafficUnsupported) {
			// A transport without the optional publisher is this machine's
			// client, not a fault of this attempt: the skip is silent.
			slog.Debug("sync: the client publishes no traffic")
			return
		}
		slog.Warn("sync: publish this machine's traffic", "err", err)
		return
	}
	r.put(KeyAccount, AccountSnapshot{ReadAt: now, Rows: rows})
	r.put(KeyTrafficPublished, now)
}
