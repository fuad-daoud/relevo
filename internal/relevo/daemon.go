package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
)

// minInterval keeps a misconfigured interval from spinning the tick.
const minInterval = 500 * time.Millisecond

// ErrReexec reports that Run stopped because a new relevo binary is ready and
// the caller should exec into it. It is not a failure: the process keeps its
// pid and its children through the exec.
var ErrReexec = errors.New("relevo daemon: re-exec onto a new binary")

// Daemon ticks on its interval and advances every binding. It is the only
// reason relevo needs a background process: the inbound leg happens after the
// mastermind's turn has ended, when no model is running to notice.
type Daemon struct {
	rt       Runtime
	interval time.Duration

	// refresh re-reads candidates.json and policy.json when they have
	// changed and returns the Runtime the tick should use. Nil means rt is
	// used as given.
	refresh func(Runtime) Runtime

	// upgrade, when set, runs after every completed tick and only while ctx
	// is live. True means a new binary passed its preflight, and Run returns
	// ErrReexec so the caller can exec into it. Nil means the daemon never
	// re-execs.
	upgrade func(ctx context.Context) bool

	// releaseRetryAt is when a release fetch that failed may be tried again.
	// The zero time means "no failure to back off from".
	releaseRetryAt time.Time

	// ingestSeen is the store revision each binding was last mirrored at, so a
	// tick whose data did not change costs one digest instead of a mirror run.
	// Process state on purpose: a restart mirrors everything once.
	ingestSeen map[string]string

	// backoff remembers servers whose prefetch failed, so a dead server costs
	// one bounded dial per backoff window instead of one per binding per tick.
	backoff remoteBackoff

	// syncMu guards the two fields below, which are the whole of the state a
	// sync trigger shares between the two goroutines that can start one.
	syncMu sync.Mutex
	// syncInFlight is whether a sync this daemon started is still running. A
	// second trigger arriving while one is in flight sets live.dirty rather
	// than starting another: the network is the slow part, and piling attempts
	// behind it only makes the backlog worse.
	syncInFlight bool
	// live is the liveness state that qualifies the slot, guarded by syncMu.
	live verb.Liveness
	// syncIdle is signalled whenever syncInFlight goes false, so a caller
	// waiting for the slot is woken rather than polling. Created once in
	// NewDaemon and never replaced, so Wait and the triggers share one lock
	// and one condition.
	syncIdle *sync.Cond
	// syncLast is when the idle window last opened. The zero time means no
	// window has run yet, so a fresh daemon syncs once and then settles.
	syncLast time.Time
	// outboxTruncatedAt is when the outbox was last emptied while sync was
	// off. The zero time means never, so a fresh daemon empties it on its
	// first tick.
	outboxTruncatedAt time.Time
	// tmpSweptAt is when this daemon last ran the tmp sweep; the zero time
	// means never, so a daemon's first tick sweeps. It is daemon state rather
	// than package state because the sweep paces one root: one timer shared
	// between daemons would let a sweep of one root postpone another root's
	// first sweep.
	tmpSweptAt time.Time
	// syncNow is the clock the idle window reads. Nil means time.Now.
	syncNow func() time.Time
	// syncVerbs is the verb runner the owner serves, set once the Daemon
	// exists. Nil means the tick drives rt.Sync exactly as before. When set,
	// the tick drives the runner it shares with the verbs, so one client is
	// reused instead of each path opening its own. Set through SetSyncVerbs:
	// cmd/relevo owns the hook installation while this type owns the field.
	syncVerbs *verb.Runner
}

// SetSyncVerbs shares the owner's verb runner with the tick, so background
// attempts reuse the client a verb built lazily instead of each path opening
// its own handle to the same remote.
func (d *Daemon) SetSyncVerbs(v *verb.Runner) {
	d.syncVerbs = v
}

// BindSyncNow installs this daemon's own trigger into its runtime, so a gate
// the daemon records through that runtime reaches the other machines at once
// rather than at the end of the export debounce. It needs no separate refresh
// hook: a refresh is applied to this runtime, so whatever it derives carries
// the trigger with it.
//
// The trigger is nil-safe from the gate write's side, so a caller that never
// bound it writes the gate and leaves the export to the ordinary triggers.
func (d *Daemon) BindSyncNow(ctx context.Context) {
	d.rt.SyncNow = func() { d.SyncNow(ctx) }
}

// syncEngine adapts the Daemon to the verb package's Engine: the clock the
// idle window reads, the signal that wakes the slot's waiters, and the trigger
// that hands a sync to the background, so the verb package never imports this
// one.
type syncEngine struct {
	d *Daemon
}

func (e syncEngine) Clock() func() time.Time {
	return e.d.syncClock()
}

func (e syncEngine) SignalIdle() {
	e.d.signalIdleLocked()
}

func (e syncEngine) QueueSync(ctx context.Context) {
	e.d.queueSync(ctx)
}

// NewDaemon returns a Daemon ticking at interval, floored at minInterval.
func NewDaemon(rt Runtime, interval time.Duration) *Daemon {
	if interval < minInterval {
		interval = minInterval
	}
	d := &Daemon{
		rt:       rt,
		interval: interval,
	}
	d.syncIdle = sync.NewCond(&d.syncMu)
	return d
}

// WithRefresh installs a per-tick refresh on the daemon. nil means the Runtime
// given to NewDaemon is used as-is. Returns d for chaining.
func (d *Daemon) WithRefresh(f func(Runtime) Runtime) *Daemon {
	d.refresh = f
	return d
}

// WithUpgrade installs a post-tick hook that decides whether to re-exec onto a
// new binary. nil means no re-exec. Returns d for chaining.
func (d *Daemon) WithUpgrade(f func(ctx context.Context) bool) *Daemon {
	d.upgrade = f
	return d
}

// TmpSweptAt and SetTmpSweptAt carry the sweep's pace across Daemons: serve
// builds a fresh Daemon per owner on every tick, so it keeps each owner's last
// sweep itself and hands it back to the next tick's Daemon.
func (d *Daemon) TmpSweptAt() time.Time { return d.tmpSweptAt }

func (d *Daemon) SetTmpSweptAt(t time.Time) { d.tmpSweptAt = t }

// WaitSyncSlot runs fn with this daemon's sync to itself, waiting its turn
// rather than dropping the work. It takes the same guard the seal hook and
// the idle window take. A trigger that finds the slot held marks it dirty for
// one rerun, because piling attempts behind a slow network does not make the
// backlog smaller; a verb waits for the in-flight attempt to finish and then
// runs, because somebody asked for it explicitly and is waiting for the
// answer.
//
// The wait is a condition rather than a poll, and the slot is released in a
// defer, so a verb that panics still frees it: a stuck flag would wedge every
// later sync on this machine.
func (d *Daemon) WaitSyncSlot(fn func()) {
	d.syncMu.Lock()
	for d.syncInFlight {
		d.syncIdle.Wait()
	}
	d.syncInFlight = true
	d.syncMu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("turso sync panicked", "panic", r, "stack", string(debug.Stack()))
		}
		d.syncMu.Lock()
		d.syncInFlight = false
		// A trigger that arrived while the verb held the slot is dropped: the
		// verb moved the same data.
		d.live.Dirty = false
		d.signalIdleLocked()
		d.syncMu.Unlock()
	}()
	fn()
}

// signalIdleLocked wakes everything waiting for the sync slot. It is called
// with syncMu held, from every site that ends a sync, so a waiter is released
// whichever kind of sync finished.
func (d *Daemon) signalIdleLocked() {
	if d.syncIdle != nil {
		d.syncIdle.Broadcast()
	}
}

// Run ticks until ctx is cancelled. A failing tick is logged and retried on
// the next interval rather than killing the daemon, because a transient
// failure must not drop every binding on the floor.
func (d *Daemon) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			// The tick runs under WithoutCancel, so a cancel mid-tick lets
			// that tick finish rather than tearing a reconcile in half. Run
			// then returns nil: it never starts another tick after
			// cancellation, and never lets the upgrade hook re-exec from
			// under a shutting-down daemon.
			tctx := context.WithoutCancel(ctx)
			if err := d.Tick(tctx); err != nil {
				slog.Error("relevo tick failed", "err", err)
			}
			if ctx.Err() != nil {
				return nil
			}
			if d.upgrade != nil && d.upgrade(ctx) {
				return ErrReexec
			}
		}
	}
}

// Tick reconciles every binding.
func (d *Daemon) Tick(ctx context.Context) error {
	if d.refresh != nil {
		d.rt = d.refresh(d.rt)
	}

	bindings, err := d.rt.Store.List()
	if err != nil {
		return fmt.Errorf("list bindings: %w", err)
	}

	// The daemon prunes dead mastermind records itself, once an hour. It runs
	// before the no-bindings early return: a machine whose sessions have all
	// ended is exactly the one left carrying stale records.
	d.safely("mastermind prune", func() { d.pruneMasterMinds() })
	// The availability and latency histories age out by SQL rather than in
	// memory: a row is deleted only by age, and no sync is running to carry
	// that away.
	d.safely("history prune", func() { d.pruneHistory() })
	// A reader round's scratch worktree is throwaway: leftovers from a crash go
	// away here, before the no-bindings early return, because a machine whose
	// readers are all gone is exactly the one left carrying them.
	d.safely("scratch sweep", func() { sweepReaderScratch(ctx, d.rt, bindings) })
	// The tmp root's own leftovers go on the same list and the same side of
	// the early return: a machine with no bindings left is exactly the one
	// still carrying yesterday's round and check temp directories, and serve
	// runs this Tick per owner, so every owner's root is swept by its own
	// daemon.
	d.safely("tmp sweep", func() {
		if d.rt.TmpRoot == "" {
			return
		}
		housekeep.SweepTick(ctx, HousekeepDeps(d.rt), bindings, &d.tmpSweptAt, d.rt.Now())
	})
	// Before the first tick of this process, every archived record the mirror
	// has not seen is ingested.
	archivedMirrorOnce.Do(func() { mirrorArchived(ctx, d.rt) })
	// A running chain whose members have left the binding list still has to be
	// swept: a member's record can be gone while the chain row says running,
	// and then there is no binding left to reconcile it on.
	d.safely("chain sweep", func() { tickChains(ctx, d.rt) })
	// A check a workflow chain started is advanced here, next to the sweep: its
	// end feeds check_closed through the same driver, under the same lock.
	d.safely("chain check sweep", func() { tickChainChecks(ctx, d.rt) })
	// A served binding's own check is advanced here, beside that sweep, because
	// a check is not a round: it runs when a client asks, and outlives the round
	// it was asked in, so it cannot wait on a round's completion marker.
	d.safely("served check sweep", func() { tickServedChecks(ctx, d.rt) })

	// The outbox is emptied here, while the machine's sync is off, and before
	// the no-bindings early return: a machine with no binding is exactly the
	// one still collecting entries nobody will drain, so returning first would
	// leave it growing for exactly as long as it stays idle.
	d.safely("outbox truncate", func() { d.truncateOutbox() })

	// The idle-tick sync runs before the no-bindings return too: a machine with
	// no binding still pulls the others' work. It only queues an attempt, so the
	// network never holds up the binding phases; their writes go out next tick.
	d.safely("turso sync", func() { d.idleSync(ctx) })

	if len(bindings) == 0 {
		d.noteRoundsOpen(nil)
		return nil
	}

	for _, b := range bindings {
		if err := d.tickOne(ctx, b); err != nil {
			slog.Error("reconcile failed", "binding", b.Name, "err", err)
		}
	}

	// The count lands after the loop, so an attempt idleSync queued earlier
	// this tick still classifies against the previous tick's count: one tick
	// of lag, which an hourly rate never notices.
	d.noteRoundsOpen(bindings)

	// The chain's staged rounds are shipped here, outside every lock, once per
	// tick and after the binding loop: a tick above may have staged the next
	// round for a remote member.
	d.safely("chain pending send", func() { chainSendPending(ctx, d.rt) })

	// A server chain is moved by the pull, outside every lock: it collects
	// every mirror chain's missing rounds and queues each chain's end delivery.
	d.safely("chain pull", func() { chainPullServers(ctx, d.rt) })

	fresh, err := d.rt.Store.List()
	if err != nil {
		slog.Warn("list bindings for metadata sync", "err", err)
		return nil
	}

	// Abandoned harness sessions are deleted here, before ingest: the delete
	// runs outside the state lock, so a harness that would resume the session
	// on its own is stopped with the tick's own liveness read already in hand.
	d.safely("reap sessions", func() { reapAll(ctx, d.rt, fresh) })

	// Local oom-queued rounds are re-admitted here, because nothing else
	// admits a local queue.
	d.safely("admit oom-queued", func() { housekeep.AdmitOOMQueued(ctx, HousekeepDeps(d.rt), fresh) })

	// Each of Tick's non-binding phases runs through safely, so a panic in
	// one cannot take the whole daemon down: the next tick tries again.
	d.safely("ingest", func() { d.ingestLiveBindings(ctx, fresh) })

	d.safely("refresh", func() { d.refreshRelease(ctx) })

	return nil
}

// tickOne is the per-binding body of Tick's whole-store pass: one critical
// section that reads the binding fresh under the lock, reconciles it, and
// writes it back. Reconcile and everything it calls take the *store.Tx rather
// than locking themselves, so a CLI command running concurrently cannot land a
// write between the read and the save.
func (d *Daemon) tickOne(ctx context.Context, b store.Binding) (err error) {
	name := b.Name
	// A panic anywhere under this binding is contained here, so Tick logs
	// "reconcile failed" and goes on to the next binding. WithLock releases
	// both of its locks through defers, so unwinding through it is safe.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("reconcile panicked", "binding", name, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("reconcile %s panicked: %v", name, r)
		}
	}()

	// The fetch runs unlocked, before the critical section: a slow or dead
	// server must not hold the state lock against every writer.
	pre := d.prefetchRemote(ctx, b)
	defer pre.release()

	// sealed is read after the lock is released, which is the only place a
	// sync may be started from.
	var sealed int
	err = d.rt.Store.WithLock(func(tx *store.Tx) error {
		loaded, err := tx.Load(name)
		if errors.Is(err, store.ErrNotFound) {
			// A `relevo unbind` landed between the caller's binding list and
			// here. That is normal use, not a failure worth logging.
			return nil
		}
		if err != nil {
			return err
		}

		// A binding written by a newer relevo is left to that relevo: this
		// binary's Save would erase every field it does not know. Format 1
		// is stored as 0, so any loaded Format above BindingFormat is a
		// newer file.
		if loaded.Format > store.BindingFormat {
			warnOnce(name, "newer-format",
				fmt.Sprintf("binding %s is format %d; this relevo knows %d; leaving it to a newer relevo",
					name, loaded.Format, store.BindingFormat),
				"binding", name, "format", loaded.Format, "known", store.BindingFormat)
			return nil
		}

		// Before Reconcile, seal every closed round whose files nothing can
		// still read. Errors are logged per binding and never fail the tick.
		//
		// A binding written before the out/ layout has its runner-output files
		// moved into out/ first, so the seal and every read below see one home.
		if moved, merr := d.rt.Store.MigrateOutLayout(name); merr != nil {
			warnOnce(name, "out-migrate", "out layout migration failed", "binding", name, "err", merr)
		} else if moved > 0 {
			slog.Info("out layout: migrated runner files into out/", "binding", name, "files", moved)
		}
		sealed = sealRounds(d.rt.Store, tx, loaded, d.rt.Policy.ArtifactMaxBytes())

		fresh := backfillMasterMindID(d.rt, loaded)

		next, err := reconcileWith(ctx, d.rt, tx, fresh, pre)
		if err != nil {
			return err
		}
		// Compared against what was on disk, not against fresh: a back-fill
		// is itself a change worth saving.
		changed, err := tx.SaveWithOverlay(next)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		// Announced only once the write above committed. This is the single
		// commit point for the daemon path, so a save that failed or was
		// skipped announces nothing and the next tick that commits the same
		// transition announces it exactly once.
		emitCommitted(ctx, d.rt, loaded, next)
		return nil
	})
	if err != nil {
		return err
	}
	// A seal that moved bytes is the moment worth syncing on: the bulk of what
	// a machine has to hand another machine is a round's files. The sync starts
	// here rather than inside the seal or the lock, and is not waited on,
	// because the seal is already committed and neither it nor the tick may
	// wait on a network.
	if sealed > 0 {
		d.queueSync(ctx)
	}
	if pre != nil && pre.Settle != nil {
		return settleCatchUp(ctx, d.rt, pre.Settle, true)
	}
	return nil
}
