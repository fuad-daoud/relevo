package relevo

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// outboxTruncateInterval is how often the outbox is emptied on a machine whose
// sync is off. It is long enough that a tick every two seconds never pays for a
// write it did not need, and short enough that an idle machine's log does not
// carry weeks of entries between two ticks.
const outboxTruncateInterval = 5 * time.Minute

// syncWindow is the shortest gap between two idle-window syncs. A tick arriving
// sooner sees the window closed and does nothing. The window exists to catch
// edits no round produced -- a config row, an installation touch, a confirm --
// so polling the network on every interval would buy nothing and cost a round
// latency each time.
const syncWindow = 5 * time.Minute

// queueSync hands a sync to the background and returns at once. The seal that
// triggered it has already committed, so the round is on disk either way: a
// network that never answers delays the other machines' copy of it and nothing
// else.
//
// A trigger arriving while one is already in flight is dropped rather than
// queued. Attempts piling up behind a slow network do not make the backlog
// smaller, and the seal path must stay cheap enough to run every tick.
func (d *Daemon) queueSync(ctx context.Context) {
	// The store is read here rather than inside the goroutine, so a refresh
	// that replaces the runtime mid-attempt cannot hand the pipeline a second
	// handle to the same file.
	st := d.rt.Store
	if st == nil {
		return
	}
	runner := d.syncRunner()
	// The enabled marker is the gate both triggers share: a machine that never
	// turned sync on must not drive a transport, and a seal is no exception.
	if !runner.On() {
		return
	}

	d.syncMu.Lock()
	if d.syncInFlight {
		d.syncMu.Unlock()
		return
	}
	d.syncInFlight = true
	d.syncMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("turso sync panicked", "panic", r, "stack", string(debug.Stack()))
			}
			d.syncMu.Lock()
			d.syncInFlight = false
			// A verb may be waiting for the slot this attempt held; it has to be
			// woken here or it waits for a trigger that already ended.
			d.signalIdleLocked()
			d.syncMu.Unlock()
		}()
		d.runSync(ctx, runner, st)
	}()
}

// idleSync runs the window's one pipeline, or does nothing at all.
//
// The window is measured from the last attempt this daemon opened rather than
// from the last one that succeeded: the breaker owns the backoff, so a failed
// attempt is retried by the next window or by the next seal, and this window
// only decides when to ask.
func (d *Daemon) idleSync(ctx context.Context) {
	runner := d.syncRunner()
	if !runner.On() {
		return
	}

	now := d.syncClock()()
	d.syncMu.Lock()
	closed := !d.syncLast.IsZero() && now.Sub(d.syncLast) < syncWindow
	if !closed {
		d.syncLast = now
	}
	d.syncMu.Unlock()
	if closed {
		return
	}
	d.queueSync(ctx)
}

// runSync is the shared body of both triggers: it drives the steady pipeline
// over the shared file and says what happened once, at a level a human reads.
//
// It asks Enabled here rather than before the slot was taken: a verb replaces
// the runner's transport under the same slot, and this read is what the slot is
// for.
func (d *Daemon) runSync(ctx context.Context, runner *relevosync.Runner, st *store.Store) {
	if !runner.Enabled() {
		return
	}
	shared, err := st.DB()
	if err != nil {
		slog.Warn("sync: open the shared database", "err", err)
		return
	}
	out := runner.SyncOnce(ctx, shared)
	if out.Err != nil {
		slog.Warn("sync failed", "err", out.Err, "attention", out.Attention)
		return
	}
	slog.Info("sync", "exported", out.Exported, "applied", out.Applied)
}

// syncRunner is the runner the tick drives: the one the owner's verbs share
// when the owner is served, so a client built lazily for a verb is reused by
// the tick rather than each path opening its own; otherwise the runtime's own
// seam, which is what a test hands a daemon.
func (d *Daemon) syncRunner() *relevosync.Runner {
	if d.syncVerbs != nil && d.syncVerbs.Runner != nil {
		return d.syncVerbs.Runner
	}
	return d.rt.Sync
}

// syncClock is the daemon's injectable now, for the idle window.
func (d *Daemon) syncClock() func() time.Time {
	if d.syncNow != nil {
		return d.syncNow
	}
	return time.Now
}

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
