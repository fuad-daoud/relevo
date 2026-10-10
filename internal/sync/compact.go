package sync

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// KeyCompact is what this machine's last compaction left behind, as
// {"at":"RFC3339","entries":N,"floor":F,"dropped":N}: when it ran, how many of
// its entries the log held afterwards, the floor it wrote and what it dropped.
// It is local state because the schedule is this machine's own.
const KeyCompact = "sync.compact"

// compactState is the marker KeyCompact holds.
type compactState struct {
	At      time.Time `json:"at"`
	Entries int       `json:"entries"`
	Floor   int       `json:"floor"`
	Dropped int       `json:"dropped"`
}

// compact drops this machine's superseded log entries when one is due.
//
// It runs after the import and the marks publish, so the floor reflects the
// marks the readers last published and this machine's own are not behind it. A
// failure is logged and left for the next tick: the exchange itself succeeded,
// and compaction is housekeeping whose retry costs nothing. It reports whether
// it compacted.
//
// The floor is skipped unless it is above the one the log already records, so a
// quiet log with no new reader progress does no work.
func (r *Runner) compact(shared *db.DB, t *StopTransport, now time.Time) bool {
	if r.Local == nil {
		return false
	}
	var last compactState
	if err := marker(r.Local, KeyCompact, &last); err != nil {
		slog.Warn("sync: read the compaction marker", "err", err)
	}
	state, err := t.ReadMarks()
	if err != nil {
		slog.Warn("sync: read the marks to compact", "err", err)
		return false
	}
	origin := shared.Origin()
	if !synclog.CompactDue(last.At, last.Entries, state.Entries[origin], now) {
		return false
	}
	floor := synclog.FloorFor(state.Marks, origin, state.Heads[origin], now)
	if floor <= state.Floors[origin] {
		return false
	}
	res, err := t.CompactOwn(floor)
	if err != nil {
		slog.Warn("sync: compact this machine's log", "err", err)
		return false
	}
	body, err := json.Marshal(compactState{At: now, Entries: state.Entries[origin] - res.Dropped, Floor: res.Floor, Dropped: res.Dropped})
	if err != nil {
		slog.Warn("sync: encode the compaction marker", "err", err)
		return false
	}
	if err := r.Local.KVPut(KeyCompact, body); err != nil {
		slog.Warn("sync: write the compaction marker", "err", err)
	}
	return true
}
