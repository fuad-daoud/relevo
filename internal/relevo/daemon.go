package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/store"
)

// minInterval keeps a misconfigured interval from spinning the tick.
const minInterval = 500 * time.Millisecond

// releaseRetryAfter is how long a failed release fetch is left alone before the
// daemon tries again (#371 §4.10): an endpoint that is down must be asked once
// an hour, not once a tick.
const releaseRetryAfter = time.Hour

// mastermindPruneInterval is how often the daemon prunes dead mastermind records
// (§4.4): at most once an hour, so a busy tick pays one kv read.
const mastermindPruneInterval = time.Hour

// mastermindPrunedAtKey is the store database's kv row naming the last prune
// (§4.4). Its value is the historical "planner.pruned_at": state already
// written.
const mastermindPrunedAtKey = "planner.pruned_at"

// ErrReexec reports that Run stopped because a new relevo binary is ready and
// the caller should exec into it (#371). It is not a failure: the process
// keeps its pid and its children through the exec.
var ErrReexec = errors.New("relevo daemon: re-exec onto a new binary")

// Daemon ticks on its interval and advances every binding. It is the only
// reason relevo needs a background process: the inbound leg happens after the
// mastermind's turn has ended, when no model is running to notice.
type Daemon struct {
	rt       Runtime
	interval time.Duration

	// refresh re-reads candidates.json and policy.json when they have
	// changed and returns the Runtime the tick should use. Nil (the
	// default) keeps today's behaviour: rt is used as given.
	refresh func(Runtime) Runtime

	// upgrade, when set, runs after every completed tick and only while ctx
	// is live. True means a new binary passed its preflight, and Run returns
	// ErrReexec so the caller can exec into it (#371 §4.5). Nil (the default)
	// keeps today's behaviour: the daemon never re-execs.
	upgrade func(ctx context.Context) bool

	// releaseRetryAt is when a release fetch that failed may be tried again.
	// The zero time means "no failure to back off from" (#371 §4.10).
	releaseRetryAt time.Time

	// ingestSeen is the store revision each binding was last mirrored at, so a
	// tick whose data did not change costs one digest instead of a mirror run.
	// It is process state on purpose: a restart mirrors everything once, and a
	// change the daemon never saw cannot hide behind a revision it wrote.
	ingestSeen map[string]string

	// syncMu guards the two fields below, which are the whole of the state a
	// sync trigger shares between the two goroutines that can start one.
	syncMu sync.Mutex
	// syncInFlight is whether a sync this daemon started is still running. A
	// second trigger arriving while one is in flight is dropped rather than
	// queued: the network is the slow part, and piling attempts behind it
	// only makes the backlog worse.
	syncInFlight bool
	// syncIdle is signalled whenever syncInFlight goes false, so a caller
	// waiting for the slot -- a sync verb -- is woken rather than polling. It is
	// created once in NewDaemon and never replaced, which is what lets Wait
	// and the triggers share one lock and one condition.
	syncIdle *sync.Cond
	// syncLast is when the idle window last opened. The zero time means no
	// window has run yet, so a fresh daemon syncs once and then settles.
	syncLast time.Time
	// syncNow is the clock the idle window reads. Nil means time.Now.
	syncNow func() time.Time
	// syncVerbs is the verb runner the owner serves, set once the Daemon
	// exists. Nil (the default) keeps today's behaviour: the tick drives
	// rt.Sync exactly as before. When set, the tick drives the runner it
	// shares with the verbs, so a client built lazily for one is reused by
	// the other instead of each path opening its own. Set through
	// SetSyncVerbs: cmd/relevo owns the hook installation while this type
	// owns the field.
	syncVerbs *VerbRunner
}

// SetSyncVerbs shares the owner's verb runner with the tick, so background
// attempts reuse the client a verb built lazily (and vice versa) instead of
// each path opening its own handle to the same remote.
func (d *Daemon) SetSyncVerbs(v *VerbRunner) {
	d.syncVerbs = v
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

// WithRefresh installs a per-tick refresh on the daemon. nil (the default)
// keeps today's behaviour: the Runtime given to NewDaemon is used as-is.
// Returns d for chaining.
func (d *Daemon) WithRefresh(f func(Runtime) Runtime) *Daemon {
	d.refresh = f
	return d
}

// WithUpgrade installs a post-tick hook that decides whether to re-exec onto a
// new binary. nil (the default) keeps today's behaviour: no re-exec.
// Returns d for chaining.
func (d *Daemon) WithUpgrade(f func(ctx context.Context) bool) *Daemon {
	d.upgrade = f
	return d
}

// WaitSyncSlot runs fn with this daemon's sync to itself, waiting its turn
// rather than dropping the work.
//
// It is the same guard the seal hook and the idle window take, and a sync verb
// takes it the same way. The difference is what a caller does when the slot is
// already held: queueSync drops a second trigger, because a tick is cheap to
// lose and piling attempts behind a slow network does not make the backlog
// smaller. A verb is not that -- somebody asked for it explicitly and is waiting
// for the answer -- so it waits for the in-flight attempt to finish and then
// runs.
//
// The wait is a condition rather than a poll, and the slot is released in a
// defer, so a verb that panics still frees it: a stuck flag would wedge every
// later sync on this machine and the statusline would keep reading a marker no
// tick writes again.
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
			// that tick finish rather than tearing a reconcile in half
			// (#371 §4.5). Run then returns nil: it never starts another
			// tick after cancellation, and never lets the upgrade hook
			// re-exec from under a shutting-down daemon.
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

// Tick reconciles every binding. It is the whole tick: the daemon has no other
// input than its interval (#303 §5.5).
func (d *Daemon) Tick(ctx context.Context) error {
	if d.refresh != nil {
		d.rt = d.refresh(d.rt)
	}

	bindings, err := d.rt.Store.List()
	if err != nil {
		return fmt.Errorf("list bindings: %w", err)
	}

	// §4.4: the daemon prunes dead mastermind records itself, once an hour. It
	// runs before the no-bindings early return: a machine whose sessions have
	// all ended is exactly the one left carrying stale records.
	d.safely("mastermind prune", func() { d.pruneMasterMinds() })
	// A reader round's scratch worktree is throwaway: leftovers from a crash go
	// away here, before the no-bindings early return, because a machine whose
	// readers are all gone is exactly the one left carrying them. It runs off
	// the list above rather than a second one.
	d.safely("scratch sweep", func() { sweepReaderScratch(ctx, d.rt, bindings) })
	// Before the first tick of this process, every archived record the mirror
	// has not seen is ingested (P3d §4.2, §4.5).
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

	if len(bindings) == 0 {
		return nil
	}

	for _, b := range bindings {
		if err := d.tickOne(ctx, b); err != nil {
			slog.Error("reconcile failed", "binding", b.Name, "err", err)
		}
	}

	// The chain's staged rounds are shipped here, outside every lock, once per
	// tick and after the binding loop: a tick above may have staged the next
	// round for a remote member, and the step is what hands it over.
	d.safely("chain pending send", func() { chainSendPending(ctx, d.rt) })

	// A server chain is moved by the pull: it collects every mirror chain's
	// missing rounds and queues each chain's end delivery, outside every lock.
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
	d.safely("admit oom-queued", func() { admitOOMQueued(ctx, d.rt, fresh) })

	// Each of Tick's non-binding phases runs through safely, so a panic in
	// one cannot take the whole daemon down (#370, spec §4.6): it is logged
	// with a stack and the next tick tries again.
	d.safely("ingest", func() { d.ingestLiveBindings(ctx, fresh) })

	d.safely("refresh", func() { d.refreshRelease(ctx) })

	// The idle-tick sync. It sits with the other non-binding phases, where
	// safely contains a failure to the phase and the next tick tries again,
	// and it is a no-op on a machine with no sync wired at all.
	d.safely("turso sync", func() { d.idleSync(ctx) })

	return nil
}

// refreshRelease refreshes the day-cached answer to "is a newer relevo out?"
// (#293). It runs after everything else Tick does and never before it: no
// reconcile decision may wait on a release check.
//
// The common path is one small file read -- a fresh cache ends it there, so a
// 2s tick stays cheap. A stale cache costs one bracketed HTTP GET, and every
// failure of that GET is swallowed and logged at debug: an offline machine
// saves nothing, writes no wrong answer, and waits out a one-hour backoff
// before trying again (#371 §4.10).
func (d *Daemon) refreshRelease(ctx context.Context) {
	if d.rt.Fetcher == nil {
		return
	}

	now := time.Now
	if d.rt.Now != nil {
		now = d.rt.Now
	}

	// A failed fetch backs off for an hour (#371 §4.10), so a down endpoint
	// is asked once an hour instead of on every tick.
	if now().Before(d.releaseRetryAt) {
		return
	}

	// The cache lives in the machine database's kv row "release-check"
	// (P3b plan §4.5), reached through the runtime's own store: for the daemon
	// that is the one shared handle it serves, so the release check adds no
	// connection of its own and no store.New per tick (D1).
	mdb, err := d.rt.Store.DB()
	if err != nil {
		slog.Debug("release check: no database", "err", err)
		return
	}

	// The release cache is this machine's last answer; the split put it in
	// the local file.
	mdb = mdb.LocalOrSelf()

	cached, ok, err := release.Load(mdb)
	if err != nil {
		slog.Debug("release check: read cache", "err", err)
		return
	}
	if !release.Stale(cached, ok, now(), release.TTL) {
		return
	}

	tag, err := d.rt.Fetcher.Latest(ctx)
	if err != nil {
		// Save nothing: a wrong or empty answer in the cache would read as
		// truth for a whole day. The next attempt waits out the backoff.
		d.releaseRetryAt = now().Add(releaseRetryAfter)
		slog.Debug("release check: fetch", "err", err)
		return
	}

	if err := release.Save(mdb, release.Cache{
		Latest:    tag,
		CheckedAt: now(),
		Source:    release.Source(),
	}); err != nil {
		slog.Debug("release check: save cache", "err", err)
		return
	}
	d.releaseRetryAt = time.Time{}
}

// tickOne is the per-binding body Tick's whole-store pass uses: one critical
// section that reads the binding fresh under the lock, reconciles it, and
// writes it back without releasing the lock. Reconcile and everything it calls
// take the *store.Tx rather than locking themselves, so a CLI command running
// concurrently cannot land a write between the read and the save.
func (d *Daemon) tickOne(ctx context.Context, b store.Binding) (err error) {
	name := b.Name
	// A panic anywhere under this binding is contained here (#370, spec
	// §4.6): it is logged with a stack and returned as an error, so Tick logs
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

	// sealed is how many files this tick's seal pass moved into the database.
	// It is read after the lock is released, which is the only place a sync
	// may be started from.
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
		// binary's Save would erase every field it does not know (#372).
		// Format 1 is stored as 0, so any loaded Format above BindingFormat
		// is a newer file. The binding is neither reconciled nor saved, and
		// the file keeps every field it had.
		if loaded.Format > store.BindingFormat {
			warnOnce(name, "newer-format",
				fmt.Sprintf("binding %s is format %d; this relevo knows %d; leaving it to a newer relevo",
					name, loaded.Format, store.BindingFormat),
				"binding", name, "format", loaded.Format, "known", store.BindingFormat)
			return nil
		}

		// P3c §4.3: before Reconcile, seal every closed round whose files
		// nothing can still read. Errors are logged per binding and never
		// fail the tick.
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
		if store.SameBinding(next, loaded) {
			return nil
		}

		if err := tx.Save(next); err != nil {
			return err
		}
		// Announced only once the write above committed (#909). This is the
		// single commit point for the daemon path, so a save that failed or
		// was skipped announces nothing and the next tick that commits the
		// same transition announces it exactly once.
		emitCommitted(ctx, d.rt, loaded, next)
		return nil
	})
	if err != nil {
		return err
	}
	// A seal that moved bytes is the moment worth syncing on: the bulk of what
	// a machine has to hand another machine is a round's files. The sync is
	// started here rather than inside the seal or the lock above, and is not
	// waited on, because the seal is already committed and neither it nor the
	// tick may wait on a network.
	if sealed > 0 {
		d.queueSync(ctx)
	}
	if pre != nil && pre.Settle != nil {
		return settleCatchUp(ctx, d.rt, pre.Settle, true)
	}
	return nil
}

// prefetchRemote reads what a remote binding's next reconcile needs from the
// server, before tickOne takes the state lock. It returns nil when the binding
// cannot be loaded or is not a live remote binding; a failed server read
// travels in the fetch's Err and is classified by the apply half. It runs
// inside tickOne's deferred recover, so a panic in the fetch is contained
// there.
//
// The binding is the caller's tick snapshot rather than a fresh load: a
// concurrent pause or unbind of a remote binding costs one wasted fetch, which
// the apply half discards against the binding it reads under the lock, and the
// saved load was most of the tick's per-binding cost.
func (d *Daemon) prefetchRemote(ctx context.Context, b store.Binding) *remoteFetch {
	if d.rt.Remote == nil {
		return nil
	}
	if !b.Builder.Remote() || b.State == store.StateDone || b.State == store.StatePaused {
		return nil
	}
	if !store.KnownState(b.State) || b.Format > store.BindingFormat {
		return nil
	}
	f := fetchRemote(ctx, d.rt, b)
	return &f
}

// archivedMirrorOnce runs the archived-record mirror feed once per process,
// before the daemon's first tick (P3d §4.5).
var archivedMirrorOnce sync.Once

// mirrorArchived feeds every archived record the mirror has not seen into the
// database.
//
// Each record is keyed by the kv row "ingested.archive.<recordID>": the key is
// put only after a successful ingest, so a failure is logged and retried by
// the next process rather than lost.
func mirrorArchived(ctx context.Context, rt Runtime) {
	if rt.DB == nil {
		return
	}

	archived, err := rt.Store.ListArchived()
	if err != nil {
		slog.Warn("mirror: list archived", "err", err)
		return
	}

	deps := IngestDeps(rt)
	for _, a := range archived {
		key := "ingested.archive." + a.RecordID
		if _, ok, kerr := rt.DB.KVGet(key); kerr != nil {
			slog.Warn("mirror: read archived cursor", "record", a.RecordID, "err", kerr)
			continue
		} else if ok {
			continue
		}

		stats, ierr := ingest.Ingest(ctx, ingest.ArchivedSource(rt.Store, a.RecordID), rt.DB, deps)
		if ierr != nil {
			slog.Warn("mirror: ingest archived", "binding", a.Binding.Name, "err", ierr)
			continue
		}
		if perr := rt.DB.KVPut(key, []byte("true")); perr != nil {
			slog.Warn("mirror: record archived cursor", "record", a.RecordID, "err", perr)
			continue
		}
		if stats != (ingest.Stats{}) {
			slog.Info("ingest", "binding", a.Binding.Name, "archived", true,
				"rounds", stats.Rounds, "events", stats.Events,
				"artifacts", stats.Artifacts)
		}
	}
}

// sweepReaderScratch removes the scratch worktrees whose reader round is no
// longer needed -- the daemon's cleanup of leftovers a crash left behind. It
// walks the tick's own binding list: a reader's scratch lives for its current
// round, so what to keep is decided by that list, not by a second read. For
// each reader binding that is not DONE it keeps the entries whose round is the
// binding's current round or later. A scratch for the current round is kept
// even before the round is open, because send makes the scratch before it logs
// the plan. Every other entry goes, including one whose binding is gone, DONE,
// or not a reader. A leftover for the current round that no round needs is
// removed by the close, the done or the unbind that already calls
// removeReaderScratch. Errors are logged per entry by SweepScratch's own join
// and never fail the tick.
func sweepReaderScratch(ctx context.Context, rt Runtime, bindings []store.Binding) {
	if rt.Git == nil {
		return
	}
	keep := map[string]int{}
	for _, b := range bindings {
		if b.Shape != store.ShapeReader || b.State == store.StateDone {
			continue
		}
		keep[b.Name] = b.Round
	}

	removed, err := SweepScratch(ctx, rt, func(name string, round int) bool {
		r, ok := keep[name]
		return ok && round >= r
	})
	if err != nil {
		slog.Warn("scratch sweep", "err", err)
	}
	for _, path := range removed {
		slog.Info("removed scratch worktree", "path", path)
	}
}

// sealRounds seals every sealable closed round of one binding (P3c §4.3):
// the round's NNN-* files become round_file rows and then leave the binding
// directory. It never fails the tick -- each error is logged and the next
// tick retries, which is also what makes a failed removal harmless.
//
// artifactMaxBytes is policy.artifact_max_mb in bytes: a round whose artifact
// directory is over it is left on disk (§3.4), so nothing is dropped, until a
// later tick sees the cap raised.
//
// The return is how many files were sealed, which is what tells the caller
// whether this pass moved bytes at all. Nothing else about the pass changes:
// the same rounds are considered, the same lines are logged, and the same
// directories are removed.
func sealRounds(st *store.Store, tx *store.Tx, b store.Binding, artifactMaxBytes int64) int {
	sealed := 0
	rounds, err := st.RoundsOnDisk(b.Name)
	if err != nil {
		slog.Warn("seal: list rounds", "binding", b.Name, "err", err)
		return sealed
	}
	for _, r := range rounds {
		drained := st.StreamDrained(b, r)
		if !store.Sealable(b, r, drained) {
			continue
		}
		if over, total := artifactCapExceeded(st, b, r, artifactMaxBytes); over {
			slog.Info("seal held: artifacts over the cap", "binding", b.Name, "round", r, "bytes", total)
			continue
		}
		n, err := tx.SealRound(b.Name, r)
		if err != nil {
			slog.Warn("seal: round", "binding", b.Name, "round", r, "err", err)
			continue
		}
		if n > 0 {
			sealed += n
			slog.Info("seal", "binding", b.Name, "round", r, "files", n)
		}
	}

	// A DONE binding is finished: once its rounds are sealed and the directory
	// holds nothing else -- no round file left to seal, no non-NNN file -- the
	// empty directory goes too, so a finished binding
	// leaves nothing on disk. os.Remove, never RemoveAll: anything still in
	// there means the directory stays. The error is ignored, like every other
	// failure in this pass.
	if b.State == store.StateDone {
		// An emptied out/ goes before the binding directory that holds it, so
		// a finished binding leaves nothing on disk.
		if entries, rerr := os.ReadDir(st.OutDir(b.Name)); rerr == nil && len(entries) == 0 {
			_ = os.Remove(st.OutDir(b.Name))
		}
		if entries, rerr := os.ReadDir(st.Dir(b.Name)); rerr == nil && len(entries) == 0 {
			_ = os.Remove(st.Dir(b.Name))
		}
	}
	return sealed
}

// safely runs one of Tick's non-binding phases, recovering a panic so that a
// single bad phase cannot take the whole daemon down (#370, spec §4.6). It logs
// at Error with phase, the panic value and the stack, and never re-panics: the
// phase simply does not happen this tick, and the next tick tries again.
func (d *Daemon) safely(phase string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tick phase panicked", "phase", phase, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	f()
}

// backfillMasterMindID is §5.6's upgrade path (#303 §5.6, last paragraph): a
// binding written before Binding.MasterMindID existed has no id, but its
// MasterMind.SessionID still names the harness session the mastermind registered
// with. When the registry knows that (kind, session), the record's id is set
// on the binding, under the lock tickOne already holds, so the channel lookup,
// the forget guard and the status row all key on the mastermind. A miss, a DONE
// binding, an empty session and a Runtime with no registry all leave the
// binding exactly as it was.
func backfillMasterMindID(rt Runtime, b store.Binding) store.Binding {
	if b.MasterMindID != "" || b.State == store.StateDone || b.MasterMind.SessionID == "" || rt.MasterMinds == nil {
		return b
	}
	rec, err := rt.MasterMinds.BySession(b.MasterMind.Kind, b.MasterMind.SessionID)
	if err != nil {
		return b
	}
	b.MasterMindID = rec.ID
	slog.Debug("mastermind backfilled", "binding", b.Name, "mastermind", rec.ID)
	return b
}

// ingestLiveBindings runs internal/ingest over every live binding whose store
// revision changed since the last run, so the database stays current with what
// the store just recorded and an unchanged binding costs one digest rather
// than a mirror transaction. d.rt.DB == nil (no database configured) is a
// no-op. A source or database error is logged at Warn and that binding is
// skipped this tick -- it never fails the tick or touches a binding, a round
// file, or a state.
//
// The revision is read before the run and recorded only after it succeeds: a
// change that lands mid-run is caught by the next tick, and a run that fails
// is retried rather than remembered as done. A binding that is gone between
// the caller's list and here is dropped from the map and skipped as normal use.
func (d *Daemon) ingestLiveBindings(ctx context.Context, bindings []store.Binding) {
	if d.rt.DB == nil {
		return
	}
	deps := IngestDeps(d.rt)
	seen := make(map[string]string, len(bindings))
	for _, b := range bindings {
		rev, rerr := d.rt.Store.Revision(b.Name)
		if errors.Is(rerr, store.ErrNotFound) {
			continue
		}
		if rerr == nil && d.ingestSeen[b.Name] == rev {
			seen[b.Name] = rev
			continue
		}
		if rerr != nil {
			// A revision that cannot be read must not stop the mirror: fall
			// through to the run and let the next tick try again.
			slog.Warn("ingest: revision", "binding", b.Name, "err", rerr)
		}
		stats, err := ingest.Ingest(ctx, ingest.StoreSource(d.rt.Store, b.Name), d.rt.DB, deps)
		if err != nil {
			slog.Warn("ingest", "binding", b.Name, "err", err)
			continue
		}
		if rerr == nil {
			seen[b.Name] = rev
		}
		if stats != (ingest.Stats{}) {
			slog.Info("ingest", "binding", b.Name,
				"rounds", stats.Rounds, "events", stats.Events,
				"artifacts", stats.Artifacts)
		}
	}
	d.ingestSeen = seen
}

// mastermindPrunedAt is the kv row planner.pruned_at's document: when the daemon
// last ran mastermind.Prune (§4.4).
type mastermindPrunedAt struct {
	At time.Time `json:"pruned_at"`
}

// mastermindPruneDue reports whether a prune last run at last (ok false when it
// never ran) is due again at now: the once-an-hour decision, pure so a test
// can pin it without a daemon (§4.4).
func mastermindPruneDue(last time.Time, ok bool, now time.Time) bool {
	if !ok {
		return true
	}
	return !now.Before(last.Add(mastermindPruneInterval))
}

// pruneMasterMinds forgets every mastermind record that is gone and that no non-DONE
// binding names (mastermind.Prune), at most once an hour (§4.4). The last run is
// the store database's kv row planner.pruned_at; each forgotten record is
// logged once. A Runtime with no registry, no usable database or a store whose
// database will not open prunes nothing and logs why.
func (d *Daemon) pruneMasterMinds() {
	rt := d.rt
	if rt.MasterMinds == nil || rt.Store == nil {
		return
	}
	kv, err := rt.Store.DB()
	if err != nil {
		slog.Warn("mastermind prune: open store db", "err", err)
		return
	}

	now := time.Now
	if rt.Now != nil {
		now = rt.Now
	}

	last, ok, err := mastermindLastPruned(kv)
	if err != nil {
		slog.Warn("mastermind prune: read last run", "err", err)
		return
	}
	if !mastermindPruneDue(last, ok, now()) {
		return
	}

	counts, err := bindingMasterMindCounts(rt.Store)
	if err != nil {
		slog.Warn("mastermind prune: list bindings", "err", err)
		return
	}

	forgotten, err := mastermind.Prune(rt.MasterMinds, rt.ProcStart, func(id string) int { return counts[id] }, false)
	if err != nil {
		slog.Warn("mastermind prune: forget", "err", err)
	}
	for _, rec := range forgotten {
		slog.Info("forgot dead mastermind", "mastermind", rec.Name, "id", rec.ID)
	}

	idle, err := mastermind.PruneIdle(rt.MasterMinds, func(id string) int { return counts[id] }, now(), false)
	if err != nil {
		slog.Warn("mastermind prune: idle", "err", err)
	}
	for _, rec := range idle {
		slog.Info("forgot idle mastermind", "mastermind", rec.Name, "id", rec.ID)
	}

	if err := recordMasterMindPruned(kv, now()); err != nil {
		slog.Warn("mastermind prune: record last run", "err", err)
	}
}

// mastermindLastPruned reads planner.pruned_at, reporting ok false when the row
// is absent (never pruned) or the database predates the kv table.
func mastermindLastPruned(kv db.KV) (last time.Time, ok bool, err error) {
	raw, ok, err := kv.KVGet(mastermindPrunedAtKey)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	var v mastermindPrunedAt
	if err := json.Unmarshal(raw, &v); err != nil {
		return time.Time{}, false, err
	}
	return v.At, true, nil
}

// recordMasterMindPruned writes planner.pruned_at after a prune attempt.
func recordMasterMindPruned(kv db.KV, at time.Time) error {
	raw, err := json.Marshal(mastermindPrunedAt{At: at.UTC()})
	if err != nil {
		return err
	}
	return kv.KVPut(mastermindPrunedAtKey, raw)
}

// bindingMasterMindCounts counts, per mastermind id, the bindings that are not DONE
// and name that mastermind: the in-use guard mastermind.Prune needs (§4.4). It is
// the same walk cmd/relevo's mastermindBindingCounts does; the daemon cannot call
// that one (it lives in package main). An unreadable store is an error, never
// an empty map.
func bindingMasterMindCounts(st *store.Store) (map[string]int, error) {
	bindings, err := st.List()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, b := range bindings {
		if b.State != store.StateDone && b.MasterMindID != "" {
			counts[b.MasterMindID]++
		}
	}
	return counts, nil
}
