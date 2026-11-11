package verb

import (
	"context"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// Engine is the daemon surface the moved hook logic drives, passed in so the verb
// package need not import the daemon.
type Engine interface {
	// Clock is the engine's injectable now. Nil means time.Now.
	Clock() func() time.Time
	// SignalIdle wakes the sync slot's waiters. Called with the slot lock held from
	// every sync end, so a waiter releases whichever kind finished.
	SignalIdle()
	// QueueSync hands a sync to the background and returns at once. A trigger
	// arriving while an attempt holds the slot marks the slot dirty instead of
	// starting a second one, so attempts never pile up behind a slow network.
	QueueSync(ctx context.Context)
	// QueueSyncNow is QueueSync for an attempt that exports whatever the export
	// gap says, and that makes the slot's rerun export when the slot is held.
	QueueSyncNow(ctx context.Context)
}

// SyncNow queues an exporting attempt without the export debounce. The gate write
// is already stamped and signalled; only the age check is bypassed, so a burst
// inside one attempt costs the one rerun the in-flight flag provides.
func SyncNow(ctx context.Context, eng Engine) {
	eng.QueueSyncNow(ctx)
}

// CountOpenRounds counts bindings holding an open round: a round in flight
// with no report back. Each binding read costs one log read; the tick's
// reconcile hands nothing back, so there is no entry list to reuse. A done
// binding counts 0 without being read even while its round stays open in the
// log: the mastermind declared its work verified, so it is not in flight for
// the idle count and no new prompt goes out for it. A paused binding keeps its
// round, so it reads like any live one.
func CountOpenRounds(st *store.Store, bindings []store.Binding) int {
	open := 0
	for _, b := range bindings {
		if b.State == store.StateDone {
			continue
		}
		entries, err := st.ReadLog(b.Name)
		if err != nil {
			slog.Warn("rounds: read the log for the idle count", "binding", b.Name, "err", err)
			continue
		}
		if roundInFlight(b, entries) {
			open++
		}
	}
	return open
}

// roundInFlight is store.RoundOpen for a local binding. A remote binding's
// round can be prompted on its server -- a chain started there sends its own
// prompts -- so its log here may hold no prompt for a round that is running;
// for it, a current round with no report yet is in flight.
func roundInFlight(b store.Binding, entries []store.LogEntry) bool {
	if b.Builder.Remote() {
		return b.Round > 0 && !store.HasKind(entries, b.Round, store.DirToMasterMind, store.KindReport)
	}
	return store.RoundOpen(entries, b.Round)
}

// countServedOpenRounds counts the open rounds on the bindings this machine
// serves to its clients. Each lives under its client's owner, so the daemon's
// own list never holds one, and on a server they are most of what it exports.
func countServedOpenRounds(st *store.Store) int {
	d, err := st.DBIfExists()
	if err != nil || d == nil {
		if err != nil {
			slog.Warn("rounds: open the database for the served idle count", "err", err)
		}
		return 0
	}
	owners, err := d.RecordOwners()
	if err != nil {
		slog.Warn("rounds: list the served owners for the idle count", "err", err)
		return 0
	}
	open := 0
	for _, owner := range owners {
		served := store.NewShared(st.Root(), owner, d)
		bindings, err := served.List()
		if err != nil {
			slog.Warn("rounds: list a client's served bindings for the idle count", "owner", owner, "err", err)
			continue
		}
		open += CountOpenRounds(served, bindings)
	}
	return open
}

// NoteRoundsOpen hands the runner the tick's open-round count, skipping the
// per-binding reads while sync is off.
func NoteRoundsOpen(runner *relevosync.Runner, st *store.Store, bindings []store.Binding) {
	if !runner.On() {
		return
	}
	runner.NoteRoundsOpen(CountOpenRounds(st, bindings) + countServedOpenRounds(st))
}
