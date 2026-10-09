package relevo

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// exportDebounce is the shortest gap between the starts of two attempts that an
// outbox entry or a freshen hint may cause. Ingest writes the shared file on
// nearly every two-second tick while a round runs, so a trigger per write
// would be an attempt per tick; fifteen seconds keeps a write's wait for the
// other machines to a quarter minute at the cost of at most four attempts a
// minute, and a burst of writes inside the gap costs one.
const exportDebounce = 15 * time.Second

// hotWindow is the idle pull window while another machine is writing. One
// attempt is a handful of calls to the remote, so 30 s is 2,880 attempts a day
// at most, and only for the minutes after something arrived; an idle machine
// never pays it.
const hotWindow = 30 * time.Second

// hotSince is how long an attempt that applied rows keeps the window hot. A
// machine whose peer just wrote is likely to see more soon; five minutes
// without a row is the cold window's own length, so a quiet peer costs nothing
// extra.
const hotSince = 5 * time.Minute

// coldWindow is the idle pull window otherwise. The window exists to catch
// edits no round produced -- a config row, an installation touch, a confirm --
// so polling the network any faster on an idle machine would buy nothing and
// cost a round latency each time.
const coldWindow = 5 * time.Minute

// syncLiveness is the state the liveness triggers share with the attempt that
// holds the slot. It lives under syncMu with the slot flag it qualifies.
type syncLiveness struct {
	// dirty is set by a trigger that found the slot held: the attempt in flight
	// may have read the outbox before the write that caused it, so the slot's
	// holder runs once more when it ends.
	dirty bool
	// startedAt is when the last attempt began. The debounce and the freshen
	// throttle measure from the start, so a slow attempt does not delay the
	// next one by its own length.
	startedAt time.Time
	// appliedAt is when the last attempt that applied rows ended. Zero means no
	// attempt has applied anything since this daemon started.
	appliedAt time.Time
}

// breakerDue reports whether the machine's breaker lets a call run now. An
// unreadable breaker reads as due: the per-call gate inside the pipeline still
// refuses, and skipping here on a transient read error would quietly stop a
// machine that is meant to be syncing.
func breakerDue(runner *relevosync.Runner) bool {
	if runner == nil || runner.Local == nil {
		return true
	}
	due, err := relevosync.NewBreaker(runner.Local).Due()
	if err != nil {
		slog.Warn("sync: read the breaker", "err", err)
		return true
	}
	return due
}

// syncSlot runs attempts for as long as triggers kept arriving during the last
// one, then releases the slot. The dirty check and the release share one
// critical section, so a trigger either lands before it and is rerun, or lands
// after the slot is free and starts its own attempt; none is lost between.
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
		d.live.dirty = false
		d.signalIdleLocked()
		d.syncMu.Unlock()
	}()
	for {
		d.runSync(ctx, runner, st)

		// The breaker is read before the lock: it is a local row read, and the
		// lock is the one a verb waits on.
		due := breakerDue(runner)
		now := d.syncClock()()
		d.syncMu.Lock()
		again := d.live.dirty && due && ctx.Err() == nil
		d.live.dirty = false
		if again {
			d.live.startedAt = now
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

// pullWindowLocked is the idle gap in force at now: hot while an attempt
// applied rows within hotSince, cold otherwise. The caller holds syncMu.
func (d *Daemon) pullWindowLocked(now time.Time) time.Duration {
	if !d.live.appliedAt.IsZero() && now.Sub(d.live.appliedAt) < hotSince {
		return hotWindow
	}
	return coldWindow
}

// noteApplied records that an attempt applied rows, which is what turns the
// window hot.
func (d *Daemon) noteApplied(applied int) {
	if applied <= 0 {
		return
	}
	now := d.syncClock()()
	d.syncMu.Lock()
	d.live.appliedAt = now
	d.syncMu.Unlock()
}

// exportIfPending queues an attempt when the outbox has entries and the last
// attempt began at least exportDebounce ago. The cheap age check runs first, so
// a tick inside the gap reads nothing.
func (d *Daemon) exportIfPending(ctx context.Context, now time.Time) {
	d.syncMu.Lock()
	started := d.live.startedAt
	d.syncMu.Unlock()
	if !started.IsZero() && now.Sub(started) < exportDebounce {
		return
	}
	if !d.outboxPending() {
		return
	}
	d.queueSync(ctx)
}

// outboxPending reports whether the machine's shared file has writes waiting
// to be exported. It reads the daemon's own handle and never opens one: a
// machine with no database has nothing waiting.
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
	return has
}

// Freshen is a hint from a reader that it wants current data: it queues an
// attempt unless one began within exportDebounce. It never waits and never
// blocks the owner connection that carried the hint, and it goes through the
// same slot and breaker as every other trigger.
func (d *Daemon) Freshen(ctx context.Context) {
	now := d.syncClock()()
	d.syncMu.Lock()
	recent := !d.live.startedAt.IsZero() && now.Sub(d.live.startedAt) <= exportDebounce
	d.syncMu.Unlock()
	if recent {
		return
	}
	d.queueSync(ctx)
}
