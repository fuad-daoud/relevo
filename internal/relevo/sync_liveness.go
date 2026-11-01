package relevo

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
)

// pullWindowLocked is the idle gap in force at now. The caller holds syncMu.
func (d *Daemon) pullWindowLocked(now time.Time) time.Duration {
	return d.live.PullWindow(now)
}

// syncSlot runs attempts for as long as triggers kept arriving during the last
// one, then releases the slot. The dirty check and the release share one
// critical section, so no trigger is lost between them: one that lands before
// it is rerun, one that lands after starts its own attempt.
func (d *Daemon) syncSlot(ctx context.Context, runner *relevosync.Runner, st *store.Store) {
	released := false
	defer func() {
		if r := recover(); r != nil {
			slog.Error("turso sync panicked", "panic", r, "stack", string(debug.Stack()))
		}
		if released {
			return
		}
		d.syncMu.Lock()
		d.syncInFlight = false
		d.live.Dirty = false
		d.signalIdleLocked()
		d.syncMu.Unlock()
	}()
	for {
		d.runSync(ctx, runner, st)

		// The breaker is read before the lock: it is a local row read, and the
		// lock is the one a verb waits on.
		due := verb.BreakerDue(runner)
		now := d.syncClock()()
		d.syncMu.Lock()
		again := d.live.Dirty && due && ctx.Err() == nil
		d.live.Dirty = false
		if again {
			d.live.StartedAt = now
		} else {
			d.syncInFlight = false
			// A verb may be waiting for the slot this attempt held; it has to
			// be woken here or it waits for a trigger that already ended.
			d.signalIdleLocked()
			released = true
		}
		d.syncMu.Unlock()
		if !again {
			return
		}
	}
}

// noteSyncOff forgets that sync was on, so the next enable opens its first
// window at once and is warm again.
func (d *Daemon) noteSyncOff() {
	d.syncMu.Lock()
	d.live.OnSince = time.Time{}
	d.syncLast = time.Time{}
	d.syncMu.Unlock()
}

// noteApplied records that an attempt applied rows, which is what turns the
// window hot.
func (d *Daemon) noteApplied(applied int) {
	if applied <= 0 {
		return
	}
	now := d.syncClock()()
	d.syncMu.Lock()
	d.live.AppliedAt = now
	d.syncMu.Unlock()
}

// exportIfPending queues an attempt when the outbox has entries and the last
// attempt began at least exportDebounce ago. The cheap age check runs first, so
// a tick inside the gap reads nothing.
func (d *Daemon) exportIfPending(ctx context.Context, now time.Time) {
	d.syncMu.Lock()
	started := d.live.StartedAt
	d.syncMu.Unlock()
	if !started.IsZero() && now.Sub(started) < verb.ExportDebounce {
		return
	}
	if !d.outboxPending() {
		return
	}
	d.queueSync(ctx)
}

// outboxPending reports whether the machine has writes waiting to be exported:
// a row in the shared file's outbox, or a document signal in the local file.
// It reads the daemon's own handle and never opens one: a machine with no
// database has nothing waiting.
func (d *Daemon) outboxPending() bool {
	if d.rt.Store == nil {
		return false
	}
	mdb, err := d.rt.Store.DBIfExists()
	if err != nil || mdb == nil {
		return false
	}
	has, err := mdb.HasOutboxEntries()
	if err != nil {
		slog.Debug("sync: probe the outbox", "err", err)
		return false
	}
	if has {
		return true
	}
	if !d.holdsSigningKey() {
		return false
	}
	signals, err := mdb.DocSignals()
	if err != nil {
		slog.Debug("sync: probe the doc signals", "err", err)
		return false
	}
	return len(signals) > 0
}

// holdsSigningKey reports whether this machine can sign a document entry. A
// locked machine keeps its document signals unexported, so counting them would
// start an attempt every debounce that can never clear them. The key is read
// through the runner the tick drives -- the one the exporter signs with.
func (d *Daemon) holdsSigningKey() bool {
	runner := d.syncRunner()
	if runner == nil || runner.Local == nil {
		return false
	}
	key, err := relevosync.SigningKey(runner.Local)
	if err != nil {
		slog.Debug("sync: read the signing key", "err", err)
		return false
	}
	return len(key) > 0
}

// Freshen is a hint from a reader that it wants current data: it queues an
// attempt unless one began within exportDebounce. It never waits and never
// blocks the owner connection that carried the hint, going through the same
// slot and breaker as every other trigger.
func (d *Daemon) Freshen(ctx context.Context) {
	now := d.syncClock()()
	d.syncMu.Lock()
	recent := !d.live.StartedAt.IsZero() && now.Sub(d.live.StartedAt) <= verb.ExportDebounce
	d.syncMu.Unlock()
	if recent {
		return
	}
	d.queueSync(ctx)
}
