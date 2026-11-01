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
}

// SyncNow queues an attempt without the export debounce. The gate write is already
// stamped and signalled; only the age check is bypassed, so a burst inside one
// attempt costs the one rerun the in-flight flag provides.
func SyncNow(ctx context.Context, eng Engine) {
	eng.QueueSync(ctx)
}

// CountOpenRounds counts bindings holding an open round: a prompt out with no
// report back. A remote binding counts too, since its prompt and report sit in
// the local log. Each binding costs one log read; the tick's reconcile hands
// nothing back, so there is no entry list to reuse.
func CountOpenRounds(st *store.Store, bindings []store.Binding) int {
	open := 0
	for _, b := range bindings {
		entries, err := st.ReadLog(b.Name)
		if err != nil {
			slog.Warn("rounds: read the log for the idle count", "binding", b.Name, "err", err)
			continue
		}
		if store.RoundOpen(entries, b.Round) {
			open++
		}
	}
	return open
}

// NoteRoundsOpen hands the runner the tick's open-round count, skipping the
// per-binding reads while sync is off.
func NoteRoundsOpen(runner *relevosync.Runner, st *store.Store, bindings []store.Binding) {
	if !runner.On() {
		return
	}
	runner.NoteRoundsOpen(CountOpenRounds(st, bindings))
}
