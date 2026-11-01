package relevo

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
)

// The availability and latency histories' retention pass. It runs beside the
// mastermind prune in the daemon's periodic maintenance and nowhere else: a
// delete by age is not work a tick should do, and it needs no sync behind it
// because an age delete is recorded nowhere. A machine with no daemon still
// shows a 30-day window, because the readers prune in memory on the way out.

// historyPruneInterval is how often the history prune runs: the same hourly
// cadence as the mastermind prune, since both are retention rather than work.
const historyPruneInterval = time.Hour

// historyPrunedAtKey is the kv row the last history prune is recorded in, on
// the machine-local file: when this machine last swept is a fact about this
// machine, not a row anything else holds.
const historyPrunedAtKey = "history.pruned_at"

type historyPrunedAt struct {
	At time.Time `json:"at"`
}

// historyPruneDue reports whether a prune last run at last (ok false when it
// never ran) is due again at now, pure so a test can pin it without a daemon.
func historyPruneDue(last time.Time, ok bool, now time.Time) bool {
	if !ok {
		return true
	}
	return !now.Before(last.Add(historyPruneInterval))
}

// pruneHistory deletes every availability event and latency sample older than
// the retention window, for every origin, at most once an hour. A Runtime with
// no shared file prunes nothing; a prune that fails leaves the marker where it
// was, so the next tick tries again rather than waiting out the hour.
func (d *Daemon) pruneHistory() {
	rt := d.rt
	if rt.DB == nil {
		return
	}

	now := time.Now
	if rt.Now != nil {
		now = rt.Now
	}

	kv := rt.DB.LocalOrSelf()
	last, ok, err := historyLastPruned(kv)
	if err != nil {
		slog.Warn("history prune: read last run", "err", err)
		return
	}
	if !historyPruneDue(last, ok, now()) {
		return
	}

	pruned, err := availability.PruneHistory(AvailabilityDeps(rt))
	if err != nil {
		slog.Warn("history prune: delete aged rows", "err", err)
		return
	}
	if pruned.Events > 0 || pruned.Samples > 0 {
		slog.Info("pruned aged history", "events", pruned.Events, "samples", pruned.Samples)
	}
	if err := recordHistoryPruned(kv, now()); err != nil {
		slog.Warn("history prune: record last run", "err", err)
	}
}

// historyLastPruned reads history.pruned_at, reporting ok false when the row is
// absent (never pruned) or the database predates the kv table.
func historyLastPruned(kv db.KV) (last time.Time, ok bool, err error) {
	raw, ok, err := kv.KVGet(historyPrunedAtKey)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	var v historyPrunedAt
	if err := json.Unmarshal(raw, &v); err != nil {
		return time.Time{}, false, err
	}
	return v.At, true, nil
}

func recordHistoryPruned(kv db.KV, at time.Time) error {
	raw, err := json.Marshal(historyPrunedAt{At: at.UTC()})
	if err != nil {
		return err
	}
	return kv.KVPut(historyPrunedAtKey, raw)
}
