package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// The daemon's local maintenance: scratch sweeps, round sealing, ingest, and
// the hourly mastermind prune Tick runs before its early return.

// mastermindPruneInterval is how often the daemon prunes dead mastermind records:
// at most once an hour, so a busy tick pays one kv read.
const mastermindPruneInterval = time.Hour

// mastermindPrunedAtKey is the store database's kv row naming the last prune.
const mastermindPrunedAtKey = "planner.pruned_at"

// sweepReaderScratch removes the scratch worktrees whose reader round is no
// longer needed, walking the tick's own binding list. A scratch for the
// current round is kept even before the round is open, because send makes the
// scratch before it logs the plan. Every other entry goes, including one whose
// binding is gone, DONE, or not a reader. Errors never fail the tick:
// SweepScratch joins them per entry.
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

	removed, err := housekeep.SweepScratch(ctx, HousekeepDeps(rt), func(name string, round int) bool {
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

// sealRounds seals every sealable closed round of one binding: the round's
// NNN-* files become round_file rows and then leave the binding directory. It
// never fails the tick: each error is logged and the next tick retries.
//
// artifactMaxBytes is policy.artifact_max_mb in bytes: a round whose artifact
// directory is over it is left on disk, so nothing is dropped, until a later
// tick sees the cap raised.
//
// The return is how many files were sealed, telling the caller whether this
// pass moved bytes at all.
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
		if over, total := view.ArtifactCapExceeded(st, b, r, artifactMaxBytes); over {
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
	// holds nothing else, the empty directory goes too, provided its worktree
	// was not kept on disk. os.Remove, never RemoveAll: anything still in
	// there means the directory stays.
	if b.State == store.StateDone && !worktreeKept(b.Worktree) {
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

func worktreeKept(wt string) bool {
	if wt == "" {
		return false
	}
	_, err := os.Lstat(wt)
	return !errors.Is(err, os.ErrNotExist)
}

// safely runs one of Tick's non-binding phases, recovering a panic so that a
// single bad phase cannot take the whole daemon down. The phase simply does
// not happen this tick, and the next tick tries again.
func (d *Daemon) safely(phase string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tick phase panicked", "phase", phase, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	f()
}

// backfillMasterMindID sets a binding's MasterMindID from the registry when it
// is empty but MasterMind.SessionID names a session the registry knows, under
// the lock tickOne already holds, so the claim lookup, the forget guard and
// the status row all key on the mastermind.
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
// revision changed since the last run, so the database stays current and an
// unchanged binding costs one digest rather than a mirror transaction. A nil
// DB is a no-op; a source or database error skips that binding this tick and
// never fails the tick.
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

// mastermindPrunedAt is when the daemon last ran mastermind.Prune.
type mastermindPrunedAt struct {
	At time.Time `json:"pruned_at"`
}

// mastermindPruneDue reports whether a prune last run at last (ok false when it
// never ran) is due again at now, pure so a test can pin it without a daemon.
func mastermindPruneDue(last time.Time, ok bool, now time.Time) bool {
	if !ok {
		return true
	}
	return !now.Before(last.Add(mastermindPruneInterval))
}

// pruneMasterMinds forgets every mastermind record that is gone and that no non-DONE
// binding names (mastermind.Prune), at most once an hour. A Runtime with no
// registry, no usable database or a store whose database will not open prunes
// nothing.
func (d *Daemon) pruneMasterMinds() {
	rt := d.rt
	if rt.MasterMinds == nil || rt.Store == nil {
		return
	}
	sdb, err := rt.Store.DB()
	if err != nil {
		slog.Warn("mastermind prune: open store db", "err", err)
		return
	}
	kv := sdb.LocalOrSelf()

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

func recordMasterMindPruned(kv db.KV, at time.Time) error {
	raw, err := json.Marshal(mastermindPrunedAt{At: at.UTC()})
	if err != nil {
		return err
	}
	return kv.KVPut(mastermindPrunedAtKey, raw)
}

// bindingMasterMindCounts counts, per mastermind id, the bindings that are not DONE
// and name that mastermind: the in-use guard mastermind.Prune needs. It is
// the same walk cmd/relevo's mastermindBindingCounts does; the daemon cannot
// call that one (it lives in package main). An
// unreadable store is an error, never an empty map.
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
