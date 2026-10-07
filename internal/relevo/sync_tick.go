package relevo

import (
	"context"
	"log/slog"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// outboxTruncateInterval is how often the outbox is emptied on a machine whose
// sync is off. It is long enough that a tick every two seconds never pays for a
// write it did not need, and short enough that an idle machine's log does not
// carry weeks of entries between two ticks.
const outboxTruncateInterval = 5 * time.Minute

// queueSync is the seal trigger's half of the sync seam. This build has no sync
// engine, so there is no change set to hand on and it does nothing: it takes no
// slot, starts no goroutine and opens no handle, and a seal costs only what the
// seal itself costs.
func (d *Daemon) queueSync(context.Context) {}

// idleSync is the window trigger's half of the sync seam. The window exists to
// catch edits no round produced, by polling the network on an interval; with no
// engine there is nothing to poll and nothing to catch, so it opens no handle
// and writes no marker.
func (d *Daemon) idleSync(context.Context) {}

// truncateOutbox empties the machine's outbox on a window while its own local
// mark says sync is off.
//
// The outbox is filled by SQL triggers on every shared-table write, whatever
// else the process does, so a machine that never syncs collects one entry per
// write for as long as it stays installed. Each entry names a row rather than
// carrying it, so dropping the entries discards no history: the reconcile that
// runs when sync is turned on re-derives what changed.
//
// The gate is the machine-local enabled mark, not the runtime's sync seam,
// because a machine with no engine wired at all is exactly the machine whose
// outbox nobody drains. The mark is read through the shared handle's local file,
// so it costs no second handle and touches nothing that leaves the machine.
func (d *Daemon) truncateOutbox() {
	if time.Since(d.outboxTruncatedAt) < outboxTruncateInterval {
		return
	}

	mdb, err := d.rt.Store.DBIfExists()
	if err != nil || mdb == nil {
		// No machine database is a machine with no outbox to empty, and a
		// handle that will not open is this tick's problem to log rather than
		// to retry inside.
		return
	}

	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		slog.Debug("outbox truncate: no local file", "err", err)
		return
	}
	state, err := relevosync.ReadState(local)
	if err != nil {
		slog.Debug("outbox truncate: read the enabled mark", "err", err)
		return
	}
	if state.Enabled {
		return
	}

	if err := mdb.TruncateOutbox(); err != nil {
		slog.Warn("outbox truncate", "err", err)
		return
	}
	d.outboxTruncatedAt = time.Now()
}
