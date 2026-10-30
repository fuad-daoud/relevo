package verb

import (
	"context"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// Engine is the daemon surface the moved hook logic drives. The verb package
// must not import the daemon's package, so the engine passes itself in: the
// clock the idle window reads, the signal that wakes the slot's waiters, and
// the trigger that hands a sync to the background.
type Engine interface {
	// Clock is the engine's injectable now, for the idle window and the
	// rebuild policy. Nil means time.Now.
	Clock() func() time.Time
	// SignalIdle wakes everything waiting for the sync slot. It is called
	// with the slot lock held, from every site that ends a sync, so a waiter
	// is released whichever kind of sync finished.
	SignalIdle()
	// QueueSync hands a sync to the background and returns at once. A trigger
	// arriving while an attempt holds the slot marks the slot dirty instead of
	// starting a second one, so attempts never pile up behind a slow network.
	QueueSync(ctx context.Context)
}

// SyncNow queues an attempt without waiting out the export debounce. It is the
// whole of what a gate write asks for: the gate is already stamped and
// signalled by the time it is called, and a person who has just recorded a
// rate limit wants the other machines to see it in seconds rather than a
// quarter of a minute from now.
//
// It bypasses only the age check. Every other rule still applies -- sync off,
// a breaker that is not due, and the slot itself -- so a burst of gate writes
// inside one attempt costs the one rerun the in-flight flag already provides
// rather than an attempt each.
func SyncNow(ctx context.Context, eng Engine) {
	eng.QueueSync(ctx)
}

// CountOpenRounds is how many bindings hold an open round: a prompt went out
// for the binding's current round and no report came back. A remote binding
// counts too, since its prompt and report sit in the local log. Each binding
// costs one log read: the tick's reconcile reads the log inside its own lock
// and hands nothing back, so there is no entry list to reuse here.
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

// NoteRoundsOpen hands the runner the tick's open-round count. Counting costs
// a log read per binding on every tick, so a machine whose sync is off, which
// books no exports, skips it.
func NoteRoundsOpen(runner *relevosync.Runner, st *store.Store, bindings []store.Binding) {
	if !runner.On() {
		return
	}
	runner.NoteRoundsOpen(CountOpenRounds(st, bindings))
}
