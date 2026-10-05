package relevo

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// syncWindow is the shortest gap between two idle-window syncs. A tick arriving
// sooner sees the window closed and does nothing. The window exists to catch
// edits no round produced -- a config row, an installation touch, a confirm --
// so polling the network on every interval would buy nothing and cost a round
// latency each time.
const syncWindow = 5 * time.Minute

// queueSync hands a sync to the background and returns at once. The seal that
// triggered it has already committed, so the round is on disk either way: a
// network that never answers delays the other machines' copy of it and
// nothing else.
//
// A trigger arriving while one is already in flight is dropped rather than
// queued. Attempts piling up behind a slow network do not make the backlog
// smaller, and the seal path must stay cheap enough to run every tick.
func (d *Daemon) queueSync(ctx context.Context) {
	if !d.rt.Sync.Enabled() {
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
		d.runSync(ctx)
	}()
}

// idleSync runs the window's one push-then-pull, or does nothing at all.
//
// The window is measured from the last attempt this daemon opened rather than
// from the last one that succeeded: there is no backoff state anywhere else, so
// a failed attempt is retried by the next window or by the next seal, and
// nothing latches.
func (d *Daemon) idleSync(ctx context.Context) {
	if !d.rt.Sync.On() {
		return
	}

	now := d.clock()()
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

// runSync is the shared body of both triggers. The runner has already written
// the markers for the outcome by the time this returns, so all that is left is
// to say what happened once, at a level a human reads.
func (d *Daemon) runSync(ctx context.Context) {
	out := d.rt.Sync.SyncOnce(ctx)
	if out.Err != nil {
		slog.Warn("turso sync failed", "err", out.Err, "attention", out.Attention)
		return
	}
	slog.Info("turso sync", "applied", out.Applied, "backlog", out.Stats.CdcOperations)
}

// clock is the daemon's injectable now, for the idle window.
func (d *Daemon) clock() func() time.Time {
	if d.syncNow != nil {
		return d.syncNow
	}
	return time.Now
}
