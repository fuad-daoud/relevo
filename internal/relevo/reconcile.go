package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
)

// nudgeNote marks the one reminder relevo sends when a builder went idle without
// writing its report file. Old logs carry nudge entries, so wait.go and
// waiting.go still filter on it.
const nudgeNote = "nudge"

func emitMutations(ctx context.Context, rt Runtime, orig, next store.Binding) {
	if rt.Hooks == nil {
		return
	}
	if next.Round > orig.Round {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventRoundStarted,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	if next.State != orig.State {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventStateChanged,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	// One builder_stalled per episode, on the zero -> set edge. The
	// clear emits nothing: the hook payload has no new fields, and a resumed
	// builder is unremarkable.
	if orig.StalledSince.IsZero() && !next.StalledSince.IsZero() {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventBuilderStalled,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	// One binding_stale per episode, on the zero -> set edge. As with
	// builder_stalled, the clear emits nothing.
	if orig.StaleSince.IsZero() && !next.StaleSince.IsZero() {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventBindingStale,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
}

// emitCommitted announces a reconcile's events once the state they describe is
// durable. emitMutations above is the pure builder over orig->saved;
// this is the one place that dispatches it, and it is deliberately silent when
// the transition never landed: a save that failed announced nothing, and a
// caller that skipped an unchanged save has nothing to announce.
//
// saved is the binding as committed, not the one the reconcile proposed, so
// what a listener reads back from the store is what it was told. The store
// lock is still held -- dispatch is fire-and-forget by design -- so the probe
// and the write that preceded it are both visible to the handler.
func emitCommitted(ctx context.Context, rt Runtime, orig, saved store.Binding) {
	if store.SameBindingSynced(orig, saved) {
		return
	}
	emitMutations(ctx, rt, orig, saved)
}

// stampStale maintains the stale clock for a NEEDS YOU binding:
// it stamps StaleSince on the moment the binding started waiting -- the halt
// time when there is one, otherwise the newest log entry -- once that moment is
// stale_after_ms old. Every other state carries no stale stamp. Send,
// resume/rebind and round close clear StaleSince, so a stamp never outlives
// the episode it was taken in.
func stampStale(rt Runtime, tx *store.Tx, b store.Binding) store.Binding {
	switch b.State {
	case store.StateNeedsYou:
	default:
		b.StaleSince = time.Time{}
		return b
	}
	if !b.StaleSince.IsZero() {
		return b
	}

	since := b.HaltAt
	if since.IsZero() {
		entries, err := tx.ReadLog(b.Name)
		if err != nil || len(entries) == 0 {
			return b
		}
		since = entries[len(entries)-1].TS
	}
	if rt.Now().UTC().Sub(since) >= rt.Policy.StaleAfter() {
		b.StaleSince = since
	}
	return b
}

// stateWarned is the daemon's warn-once memory, keyed by binding name plus the
// reason. It is a log dedupe and nothing more: the skipped binding is
// still skipped on every tick. Keying on the reason as well as the name lets
// one binding carry both a newer-format and an unknown-state warning.
var stateWarned sync.Map

// warnOnce logs msg at Warn the first time binding+reason is seen in this
// process. The variadic args are structured attributes, as every other slog
// call in the package uses.
func warnOnce(binding, reason, msg string, args ...any) {
	if _, seen := stateWarned.LoadOrStore(binding+"\x00"+reason, struct{}{}); seen {
		return
	}
	slog.Warn(msg, args...)
}

// Reconcile advances one binding: its consults, then its builder's round
// (headless process or remote poll), and finally any pending mastermind payload.
//
// Reconcile does NOT persist anything: it returns the binding and the caller
// must `tx.Save` it before releasing the lock. Everything it calls takes the
// same tx rather than locking itself, which is what lets the caller hold one
// critical section across the whole read-reconcile-write.
func Reconcile(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (out store.Binding, err error) {
	return reconcileWith(ctx, rt, tx, b, nil)
}

// reconcileWith is Reconcile with an optional prefetched remote view: a nil
// pre makes a remote binding fetch inline, as Reconcile always did.
func reconcileWith(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, pre *remoteFetch) (out store.Binding, err error) {
	// An unknown State is one a newer relevo wrote. Reconciling it
	// as live would drive a builder the newer relevo is already driving, so
	// the binding is returned exactly as it is and nothing at all runs: no
	// consults, no builder handling, and no save by the caller. The warning
	// repeats only once per process.
	//
	// It comes before every other step -- including the consult pass below --
	// because "left alone" must mean byte-for-byte unchanged.
	if !store.KnownState(b.State) {
		warnOnce(b.Name, "unknown-state",
			fmt.Sprintf("binding %s has unknown state %q; leaving it to a newer relevo", b.Name, string(b.State)),
			"binding", b.Name, "state", string(b.State))
		return b, nil
	}

	defer func() {
		if err == nil {
			// The stale clock is stamped on whatever state the tick
			// settled on, before the caller saves and announces it: the
			// stamp is part of the transition being committed, not an event of
			// its own, so it is set here and dispatched by the caller.
			out = stampStale(rt, tx, out)
		}
	}()
	// Consults reconcile before the builder is located, and before the DONE
	// gate below, because they are orthogonal to both: a reviewer reading a
	// diff has no stake in whether the builder's pane still exists, nor in
	// whether the mastermind has already called the work done. Reconcile returns
	// early when the binding is done, when the builder is gone (below), and on
	// the round-cap halt, and none of those should stop a consult finishing.
	//
	// The DONE case is the one that bites: a consult never advanced past
	// ConsultRunning would otherwise never be observed by a tick again, and gc
	// then removes the binding and its consults with it.
	//
	// On those early-return paths deliverAndSettle is skipped, so queued
	// findings wait on disk and the background wait's delivery retrieves them.
	// That is the opposite of what a halt does: a halt queues an entry and
	// settles it in the same tick (haltAndSettle), so nothing about the halt
	// is a precedent for leaving an entry pending here. An entry a push route
	// already admitted is the exception: no other route can take it (an
	// admitted entry is not claimable), so the read-back runs here or it never
	// runs at all.
	b, err = consult.Reconcile(ctx, consultDeps(rt), tx, b)
	if err != nil {
		return b, err
	}

	// A halt notification a close could not write is retried here, before
	// anything reads the round: it is the only thing this binding still owes,
	// and the round it is about has already advanced, so nothing below would
	// reach it. It sits ahead of the DONE gate because a binding finished or
	// paused after the halt still owes its MasterMind the entry.
	//
	// A failure here is held rather than returned at once, because the DONE
	// gate below settles a payload no other route can take and a log refusing
	// one append would otherwise repeat the refusal every tick and settle
	// nothing. queueOwedHalt leaves the marker on the binding it returns, so
	// the owed entry stays owed and a later tick writes it; the tick still
	// reports the failure, and reports it after the settle has run.
	b, owedErr := queueOwedHalt(ctx, rt, tx, b)

	if b.State == store.StateDone || b.State == store.StatePaused {
		// No new push may start for a done or paused binding, but a payload a
		// push route admitted before the state changed must still be settled.
		var derr error
		if b, _, derr = delivery.ConfirmAdmitted(ctx, deliveryDeps(rt), tx, b); derr != nil {
			return b, derr
		}
		return b, owedErr
	}
	if owedErr != nil {
		return b, owedErr
	}

	if b.Builder.Remote() {
		return reconcileRemote(ctx, rt, tx, b, pre)
	}

	// Every local binding reaching this line is headless: a local builder is
	// only ever a process relevo runs per round. The headless path below is
	// its own tick.
	return reconcileHeadless(ctx, rt, tx, b)
}

// checkRoundTimeout flags a builder that has been working past its budget. It
// never kills anything -- the human decides whether to nudge, reset or switch.
//
// The bool reports whether it halted, so the caller can skip delivery the way
// the round cap does. It is returned rather than inferred from the state,
// because a binding can arrive here already NeedsYou for an unrelated reason
// and must still have its pending payload delivered.
//
// The halting tick scans for a rate limit first: a match switches
// the builder instead of halting, and the switch does not count toward
// max_switches.
func checkRoundTimeout(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, error) {
	if b.RoundStartedAt.IsZero() || b.RoundTimeoutMS <= 0 {
		return b, false, nil
	}

	budget := time.Duration(b.RoundTimeoutMS) * time.Millisecond
	if rt.Now().UTC().Sub(b.RoundStartedAt) < budget {
		return b, false, nil
	}

	if b.HaltNotifiedRound != b.Round {
		text, resetMS := limitScan(ctx, rt, b)
		next, _, handled, err := gateOnLimit(ctx, rt, tx, b, text, resetMS, true)
		if handled {
			return next, true, err
		}
	}

	next, err := haltAndSettle(ctx, rt, tx, b,
		fmt.Sprintf("%s: round %d has run past %s", b.Name, b.Round, budget))

	return next, true, err
}

// deliverAndSettle attempts any pending delivery. The binding is left pending
// when no route can take the payload -- DeliverPending records why, and
// `relevo wait` or the push holder's own drain delivers it later.
func deliverAndSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.Owner != "" {
		// Owned by a remote client: there is no mastermind. Payloads stay
		// queued; the owner reads them over the wire.
		return b, nil
	}
	next, got, err := delivery.DeliverPending(ctx, deliveryDeps(rt), tx, b)
	if err != nil {
		return b, err
	}
	b = next

	if got.Delivered && got.Reason != "" {
		slog.Info("payload delivered", "binding", b.Name, "round", got.Round, "route", got.Route, "reason", got.Reason)
	}

	return b, nil
}

// HasEntry reports whether the log already contains a message of that shape.
// It reads store's shared definition rather than repeating it.
func HasEntry(entries []store.LogEntry, round int, dir store.Direction, kind store.Kind) bool {
	return store.HasKind(entries, round, dir, kind)
}
