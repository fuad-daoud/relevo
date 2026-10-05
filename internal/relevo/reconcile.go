package relevo

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
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
	// One builder_stalled per episode, on the zero -> set edge (#252). The
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
	// One binding_stale per episode, on the zero -> set edge (#135). As with
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
// durable (#909). emitMutations above is the pure builder over orig->saved;
// this is the one place that dispatches it, and it is deliberately silent when
// the transition never landed: a save that failed announced nothing, and a
// caller that skipped an unchanged save has nothing to announce.
//
// saved is the binding as committed, not the one the reconcile proposed, so
// what a listener reads back from the store is what it was told. The store
// lock is still held -- dispatch is fire-and-forget by design -- so the probe
// and the write that preceded it are both visible to the handler.
func emitCommitted(ctx context.Context, rt Runtime, orig, saved store.Binding) {
	if store.SameBinding(orig, saved) {
		return
	}
	emitMutations(ctx, rt, orig, saved)
}

// stampStale maintains the stale clock (#135) for a NEEDS YOU binding:
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
// reason (#372). It is a log dedupe and nothing more: the skipped binding is
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
	// An unknown State is one a newer relevo wrote (#372 §4.1). Reconciling it
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
			// The stale clock (#135) is stamped on whatever state the tick
			// settled on, before the caller saves and announces it (#909): the
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
	// only ever a process relevo runs per round (#99, #303). Spec §5.1 is its
	// own tick.
	return reconcileHeadless(ctx, rt, tx, b)
}

// haltBinding stops relaying and asks for a human, exactly once per round.
//
// The dedup keys on HaltNotifiedRound rather than on State because State is
// rewritten by other steps of the same tick, which is what made every earlier
// State-keyed guard log once per poll instead of once. Per round is also the
// behaviour a human wants: one log line per round that goes wrong.
//
// The same key decides the queued entry: setting the state word and nothing
// more left a MasterMind with no push route never hearing about the halt.
// haltBinding therefore needs the caller's tx to write that entry, which is
// why every halt site threads one through -- the entry has to land in the same
// critical section as the halt itself, or a crash between them leaves a halted
// binding the log never mentioned.
//
// entryRound is the round the entry is filed under, which is b.Round at every
// site but one: queueReport's post-advance halts have already advanced the
// binding, and file under the round whose artifact size or scope verdict caused
// the halt. It is threaded rather than inferred for that reason -- see
// closedRoundHalt.
//
// The reason is written inside the notification guard rather than beside it.
// The guard is what decides whether this halt is told to anyone, and a halt it
// dedupes queues no entry -- so a reason written on that path is one no entry
// ever carried. That is not only a lost line: a post-advance halt (the reader
// artifact cap, a scope refusal) stamps the key with the round the binding has
// just advanced to, so the next tick's halt of that round -- the round cap, the
// round timeout -- finds the key already equal, is deduped, and would otherwise
// overwrite the reason the mastermind actually received with a reason nothing
// says. HaltAt is written with it for the same reason: it marks when the
// notified halt began.
func haltBinding(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entryRound int, message string) (store.Binding, error) {
	return haltBindingKind(ctx, rt, tx, b, entryRound, message, "")
}

// haltBindingKind is haltBinding for a halt that names the episode behind it.
// The kind is stamped inside the notification guard, not on the binding handed
// in: a halt the guard dedupes queues no entry and tells nobody, so a kind set
// for it would attach itself to a reason that already went out and answer for
// an episode that never did. Every halt that does notify stamps its own kind,
// the empty one included, so a kind left by an earlier episode is replaced by
// the kind this halt actually is.
//
// The stamping cannot move out to the caller for the reason HaltAt cannot:
// haltAndSettle reads the binding this returns, and a field set on the result
// never reaches disk.
func haltBindingKind(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entryRound int, message, kind string) (store.Binding, error) {
	text := strings.TrimPrefix(message, b.Name+": ")

	if b.HaltNotifiedRound != b.Round || supersedesRemoteHalt(b.RemoteHaltKind, kind) {
		if b.Halt != text || b.HaltAt.IsZero() {
			b.HaltAt = rt.Now().UTC()
		}
		b.Halt = text
		b.RemoteHaltKind = kind

		slog.Info("binding halted", "binding", b.Name, "round", b.Round, "reason", message)

		b.HaltNotifiedRound = b.Round

		// Queued after the key is stamped, not before: the stamp is the dedup,
		// so an entry written and then re-entered would queue twice.
		if err := queueHalt(ctx, rt, tx, b, entryRound, text); err != nil {
			return b, err
		}
	}

	b.State = store.StateNeedsYou

	return b, nil
}

// clearHaltFields drops the halt a binding carries, every field of it: the
// reason a human reads, the time it began, the per-round notification key and
// the kind naming the episode. Clearing a halt means clearing all four, because
// each answers a question the binding no longer has. A kind left behind names
// an episode for a halt that is gone: the next answering view then clears a
// halt nothing wrote. A key left behind silences the next halt of the round it
// still names.
func clearHaltFields(b store.Binding) store.Binding {
	b.Halt = ""
	b.HaltAt = time.Time{}
	b.HaltNotifiedRound = 0
	b.RemoteHaltKind = ""
	return b
}

// closedRoundHalt is haltBinding for a caller whose b.Round has already moved
// past the round the halt is about: the entry is filed under closedRound, the
// round whose artifact size or scope verdict decided it.
//
// The round is passed, not read off the binding, because the binding cannot
// say which round closed once it has advanced. It matters because
// PullPendingThroughEntries answers `<= round` and DefaultWaitRound waits on
// the round the builder was sent, so an entry filed under N+1 is one nobody
// pulls while the wait on N is still running -- and WaitOutcome has already
// returned Done on N's report by then, so the halt text is never seen. The
// dedup key stays b.Round: HaltNotifiedRound is the per-round notification
// stamp, and a halt of the new round is a new notification.
func closedRoundHalt(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, closedRound int, message string) (store.Binding, error) {
	return haltBinding(ctx, rt, tx, b, closedRound, message)
}

// closeHaltOrOwe halts the binding for a round that has already closed, and
// records the notification as owed when the entry cannot be written.
//
// The halt itself is never in question: the binding keeps the text, the stamp
// and the state word whatever the log says, because the caller saves what it
// returns and a caller that returned the failure instead would save nothing at
// all -- the round close that precedes this halt has already appended the
// round's report, and a log that refuses one append may well have taken the
// rest. The round would stay closed on disk with the binding still on it, and
// no later tick would decide to halt it again.
//
// The owed entry is keyed on its own binding field rather than on
// HaltNotifiedRound: the failed tick stamped that, so re-entering haltBinding
// would queue nothing, and the marker is what a later tick reads to know the
// notification is still outstanding.
func closeHaltOrOwe(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, closedRound int, message string) store.Binding {
	next, err := closedRoundHalt(ctx, rt, tx, b, closedRound, message)
	if err == nil {
		return next
	}
	slog.Warn("halt entry owed to a later tick", "binding", b.Name, "round", closedRound, "err", err)
	// haltBinding returns before the state word when the queue fails, on the
	// understanding that its caller saves nothing. This caller goes on, so the
	// halt is finished here: next.Halt is the name-stripped text the entry
	// would have carried, and the stamp it already set is what keeps the owed
	// entry from being queued twice.
	next.State = store.StateNeedsYou
	next.OwedHalt = &store.OwedHalt{Round: closedRound, Text: next.Halt}
	return next
}

// queueOwedHalt writes the entry a close owed and clears the marker, so the
// halt is notified exactly once however long the log refused.
//
// A failure here is returned, and the caller saves nothing: the marker is
// already on disk from the tick that set it, so a log that is still refusing
// costs the notification a tick rather than losing it.
//
// It is keyed on the marker alone, which is why it runs on a binding whose
// HaltNotifiedRound already equals its Round -- that stamp is what the failed
// queue left behind, and this is the tick that makes it true.
//
// The marker clears the moment the entry is written, but this tick's own save is
// what makes that stick, and the tick can still fail below here: the entry is
// already on disk while the binding is not. The next tick reads the same marker
// and would write a second halt for the same round, so the retry checks the log
// for the entry first and only the marker is cleared when it is already there.
func queueOwedHalt(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.OwedHalt == nil {
		return b, nil
	}
	owed := *b.OwedHalt
	if !haltEntryWritten(tx, b.Name, owed) {
		if err := queueHalt(ctx, rt, tx, b, owed.Round, owed.Text); err != nil {
			return b, fmt.Errorf("queue owed halt for round %d: %w", owed.Round, err)
		}
	}
	b.OwedHalt = nil
	return b, nil
}

// haltEntryWritten reports whether the owed halt's entry is already in the log,
// so a retry after a tick that wrote the entry and then failed to save does not
// queue it a second time.
//
// The round and the reason together are the identity: the reason is what the
// entry's Note carries and what b.Halt shows a human, and the round is what a
// MasterMind waits on, so an entry matching both is the one this marker owes. A
// different reason for the same round is a different halt and is still queued --
// which is what a server's own halt for a round the unreachable episode already
// notified is.
func haltEntryWritten(tx *store.Tx, name string, owed store.OwedHalt) bool {
	if tx == nil {
		return false
	}
	entries, err := tx.ReadLog(name)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Kind == store.KindHalt && e.Direction == store.DirToMasterMind &&
			e.Round == owed.Round && e.Note == owed.Text {
			return true
		}
	}
	return false
}

// queueHalt writes the one to_planner entry a halt owes the MasterMind for its
// round, and is the whole of the halt notification: payload plus a pointer to
// the verb that resolves it, no file.
//
// The payload is the halt reason as `b.Halt` records it -- the name-stripped
// text, which is also the line view.WaitingOn shows a human -- followed by the
// same `relevo status --name <binding>` pointer Waiting.Hint carries, so the
// reader of the entry and the reader of the waiting line are pointed at one
// place.
//
// Path is empty on purpose: a halt wrote no artifact, and pointing at a file
// that does not exist would send the MasterMind to read nothing. That also
// makes PushText return the payload whole -- the push expansion admits only
// the report, findings and edge kinds -- so there is no truncation pointer to
// render.
//
// The caller owns the dedup: this queues unconditionally, and haltBinding is
// what gates it on HaltNotifiedRound, so the key that keeps a second halt of
// the same round from repeating an entry is the key that keeps it from
// repeating the log line.
//
// entryRound is the round the entry is filed under rather than b.Round, for the
// reason closedRoundHalt gives.
func queueHalt(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entryRound int, text string) error {
	if tx == nil {
		// A caller with no transaction cannot queue. Refusing loudly would
		// fail the whole tick over a notification, so the halt stands and the
		// state word is still set; the entry is lost, which is the old
		// behaviour rather than a broken one.
		slog.Warn("halt not queued: no transaction", "binding", b.Name, "round", b.Round)
		return nil
	}

	return delivery.Queue(ctx, deliveryDeps(rt), tx, b.Name, store.LogEntry{
		TS:        rt.Now().UTC(),
		Round:     entryRound,
		Direction: store.DirToMasterMind,
		Kind:      store.KindHalt,
		Note:      text,
		Payload:   fmt.Sprintf("%s. %s", strings.TrimSuffix(text, "."), haltPointer(b.Name)),
	})
}

// haltAndSettle is haltBinding plus the delivery attempt, and it is what every
// halt path that ENDS a reconcile tick calls.
//
// A halt path returns straight out of the tick, and it has to deliver on the
// way: the halt queues an entry, and a tick that returns without a delivery
// attempt leaves that entry pending with nothing scheduled to take it. The halted
// binding is reconciled again on the next tick, so the entry is not lost forever
// -- but "eventually, if the next tick happens to halt again" is not a
// notification. So the halt path attempts delivery itself, through the same
// route every other payload takes.
//
// It is safe where the tick already delivered: DeliverPending on a binding with
// nothing pending returns Empty without touching the log, so calling this ahead
// of a later deliverAndSettle is a no-op in that case.
//
// queueReport's cap and scope halts deliberately do not come through here.
// They sit inside a round close, and the close's own caller delivers
// immediately afterwards; settling inside queueReport would deliver the round's
// report from the middle of its own close, before the chain bookkeeping below
// the halt has run.
func haltAndSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, message string) (store.Binding, error) {
	return haltAndSettleKind(ctx, rt, tx, b, message, "")
}

// haltAndSettleKind is haltAndSettle for a halt that names the episode behind
// it: the kind travels with the halt into haltBindingKind, so it is stamped
// only when the halt actually notifies.
func haltAndSettleKind(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, message, kind string) (store.Binding, error) {
	next, err := haltBindingKind(ctx, rt, tx, b, b.Round, message, kind)
	if err != nil {
		return next, err
	}
	return deliverAndSettle(ctx, rt, tx, next)
}

// haltPointer is the verb that resolves a halt: the same one view.WaitingOn
// hands a human in Waiting.Hint, named here so the queue needs no import of
// view.
func haltPointer(name string) string {
	return "relevo status --name " + name
}

// bindingSwitchable is the one definition of "the daemon will bring a builder
// back by itself", which is the only thing that excuses a broken binding from
// owing its MasterMind an entry. It mirrors view.WaitingOn's own test, and the
// two must agree: WaitingOn answers ok=false for a switchable broken binding
// because the fault resolves itself, and queueBrokenHalt queues for every
// binding WaitingOn does call waiting. Duplicated rather than exported for the
// same reason view carries its own copy.
//
// The PID clause is what makes that agreement reachable. A round's builder
// candidate and RoundStartedAt survive the switch that failed to replace them,
// so a mid-round switch whose spawn failed looks switchable while having no
// process at all -- and no tick retries it (reconcileHeadless returns at the
// PID == 0 branch without acting). That binding is waiting on a human, not on
// the daemon, so with no process it is not switchable. The spawn_failed ledger
// entry the failed resolve already recorded still gates the bad candidate, so
// self-healing is preserved: the next human send picks a different one.
func bindingSwitchable(b store.Binding) bool {
	return b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero() && b.Builder.PID != 0
}

// queueBrokenHalt is the broken-binding half of queueHalt. A binding that is
// StateBroken and not switchable is waiting on a human, exactly as a halted one
// is, so it owes the same entry -- under the same per-round key, because a
// broken binding never passes through haltBinding to stamp one.
//
// reason is the named-prefixed text the caller logs, and it is stripped here the
// way haltBinding strips it: the entry's Note has to be the same string b.Halt
// records, or the owed-entry retry that reads the note back does not recognise
// the entry it is looking for, and the reason the entry carries is not the one a
// human reads on the binding.
//
// It returns the binding because the dedup stamp is part of the answer: the
// caller saves what comes back, and the next tick must see the same
// HaltNotifiedRound key haltBinding uses for its own log line. The bool reports
// whether an entry was actually queued, so a caller that writes the reason onto
// the binding does it only when an entry carries that reason: a binding the
// daemon can still switch, or one already notified this round, queues nothing
// and must keep whatever reason it already had.
func queueBrokenHalt(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reason string) (store.Binding, bool, error) {
	if bindingSwitchable(b) {
		return b, false, nil
	}
	if b.HaltNotifiedRound == b.Round {
		return b, false, nil
	}
	text := strings.TrimPrefix(reason, b.Name+": ")
	if err := queueHalt(ctx, rt, tx, b, b.Round, text); err != nil {
		return b, false, err
	}
	b.HaltNotifiedRound = b.Round
	return b, true, nil
}

// checkRoundTimeout flags a builder that has been working past its budget. It
// never kills anything -- the human decides whether to nudge, reset or switch.
//
// The bool reports whether it halted, so the caller can skip delivery the way
// the round cap does. It is returned rather than inferred from the state,
// because a binding can arrive here already NeedsYou for an unrelated reason
// and must still have its pending payload delivered.
//
// The halting tick scans for a rate limit first (spec §5): a match switches
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
		text := limitText(ctx, rt, b)
		next, _, handled, err := gateOnLimit(ctx, rt, tx, b, text, true)
		if handled {
			return next, true, err
		}
	}

	next, err := haltAndSettle(ctx, rt, tx, b,
		fmt.Sprintf("%s: round %d has run past %s", b.Name, b.Round, budget))

	return next, true, err
}

// noteScraped marks a report entry whose body is a terminal capture, not
// the builder's own file. A scraped body is never tail-parsed (#221).
const noteScraped = "scraped"

// joinNotes space-joins the non-empty ones, so a report entry's note can
// carry both an existing reason (e.g. "noreport") and the escape annotation
// (#192) without either overwriting the other.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " " + b
	}
}

// closeOnMarker closes an open round when the builder's completion marker
// (Store.DonePath) exists. It is the one place the round decides "the builder
// says it is finished".
//
// With a gate configured (#132) it first runs the gate across ticks: gating
// is true while the gate is running and the round must be left alone --
// both callers return early without acting on the builder in any way (no
// nudge, no "exited without a report" switch, no timeout halt). closed is
// true when the round was closed this tick. closed and gating are never
// both true.
//
// extraNote is appended (via joinNotes) to whichever note this close would
// otherwise write -- "" for the pane path, and the escape annotation (#192)
// computed by the headless path from escapeCheck before this call, since
// queueReport clears RoundBaselineTree and the comparison must happen while
// it is still on the binding.
//
// Preconditions: the round is open -- a plan was sent for b.Round and no
// report has been queued for it.
// Postconditions:
//   - marker absent: closed and gating are false, b is returned unchanged, nothing written.
//   - gate running: gating is true, the binding carries the started/updated GateRun.
//   - marker and report present: the round closes normally (note extraNote,
//     plus "gate=<result>" and the gate's payload line when gated).
//   - marker present, report absent: the round closes with note
//     joinNotes("noreport", extraNote) and a payload saying so. The terminal
//     is never read: the builder said it was done, and a scrape would be a
//     worse artefact than an honest gap.
//
// Errors are gateStep's or queueReport's, wrapped; the round stays open and
// the next tick retries, since the marker is still on disk.
//
// The gate record is returned alongside the close (nil when no gate ran) so
// the caller can act on a failure after the report is queued (#132 part 2).
func closeOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, extraNote string) (store.Binding, bool, bool, *store.GateRecord, error) {
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.DonePath(b.Name, b.Round)); !ok {
		return b, false, false, nil, nil
	}

	// A reader has no check, so the gate never runs for it: done is true with
	// no record, exactly as an ungated writer.
	var (
		done    bool
		rec     *store.GateRecord
		verdict scopeVerdict
	)
	if b.Shape == store.ShapeReader {
		done = true
	} else if b.GateRun == nil {
		// Scope fires before the gate (#801): a refusal closes the round
		// without running gateStep at all -- no gate record, no KindGate
		// entry, no repair round, Regate untouched.
		verdict = judgeRoundScope(ctx, rt, b)
		if verdict.Refused {
			done = true
		} else {
			var err error
			b, done, rec, err = gateStep(ctx, rt, tx, b)
			if err != nil {
				return b, false, false, nil, fmt.Errorf("close round on marker: gate: %w", err)
			}
			if done && rec != nil {
				// The tree can move while the gate runs: judge again on the
				// tick the gate finishes.
				verdict = judgeRoundScope(ctx, rt, b)
			}
		}
	} else {
		var err error
		b, done, rec, err = gateStep(ctx, rt, tx, b)
		if err != nil {
			return b, false, false, nil, fmt.Errorf("close round on marker: gate: %w", err)
		}
		if done && rec != nil {
			verdict = judgeRoundScope(ctx, rt, b)
		}
	}
	if !done {
		return b, false, true, nil, nil
	}

	note := extraNote
	gateSuffix := ""
	if rec != nil {
		note = joinNotes(note, "gate="+rec.Result)
		var tail []string
		if rec.Result == "fail" {
			tail = tailLines(rt.Store.ReadFile, rec.LogPath, gateTailLines)
		}
		gateSuffix = "\n" + gateLine(b.Name, b.Round, *rec, tail)
	}

	reportPath, serr := writeReaderSummary(rt, b)
	if serr != nil {
		slog.Warn("reader output not written", "binding", b.Name, "round", b.Round, "err", serr)
	}
	if _, _, ok, _ := rt.Store.StatFile(reportPath); ok {
		slog.Info("round closed by marker", "binding", b.Name, "round", b.Round)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
			fmt.Sprintf("The runner finished round %d. %s", b.Round, closeClause(rt, b, b.Round))+gateSuffix, joinNotes("", note), rec, nil, nil, nil, "", false, verdict)
		if err != nil {
			return b, false, false, nil, fmt.Errorf("close round on marker: %w", err)
		}
		return next, true, false, rec, nil
	}
	slog.Warn("round closed by marker without a report", "binding", b.Name, "round", b.Round, "note", "noreport")
	next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
		fmt.Sprintf("Builder wrote its completion marker for round %d but wrote no %s.", b.Round, outputWord(b.Shape))+gateSuffix, joinNotes("noreport", note), rec, nil, nil, nil, "", false, verdict)
	if err != nil {
		return b, false, false, nil, fmt.Errorf("close round on marker: %w", err)
	}
	return next, true, false, rec, nil
}

func queueReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, path, payload, note string, gate *store.GateRecord, usage *usage.Usage, rusage *store.Rusage, prior *usage.Tokens, fallbackOutcome string, stopped bool, verdict scopeVerdict) (store.Binding, error) {
	// The round this close is closing: the cap check below sizes that round's
	// artifact directory after b.Round has advanced.
	closedRound := b.Round
	// The chain this close belongs to, when it belongs to one. It is read
	// before the round advances, so the event names the round that closed;
	// the wiring advances the chain after the advance below. chainRunning is
	// the consumption decision: a running chain's member close is consumed
	// rather than delivered.
	chainRow, chainErr := tx.ChainByMember(b.Name)
	chainRunning := chainErr == nil && chainRow.Status == string(chain.StatusRunning)
	// A reader's report is its artifact directory's summary.md: it is written
	// here from the runner's final message when the runner wrote none itself,
	// and it is the path the report entry records. A writer's report is the
	// flat NNN-report.md the caller computed.
	if p, err := writeReaderSummary(rt, b); err != nil {
		slog.Warn("reader output not written", "binding", b.Name, "round", b.Round, "err", err)
	} else {
		path = p
	}
	// The round's throwaway worktree goes away at close, after the summary is
	// on disk: the artifact directory lives under the binding, not the scratch.
	removeReaderScratch(ctx, rt, b, b.Round)

	now := rt.Now().UTC()
	roundStart := b.RoundStartedAt

	// A round that closes while a stop was requested is the stop succeeding
	// (#138): the note says so, and a KindStop entry follows the report below.
	// This is the stored request's fact, not the caller's `stopped` parameter:
	// stop_test.go pins exactly one stopped/killed entry per stop, and the
	// parameter drives only the chain event below.
	stopRequested := !b.StopRequestedAt.IsZero()
	if stopRequested {
		note = joinNotes(note, "stopped")
	}

	body, _ := rt.Store.ReadFile(path)
	var (
		tail   reporttail.Tail
		ok     bool
		reject string
	)
	outcome := reporttail.OutcomeUnstructured
	if note != noteScraped {
		// A scraped body is a terminal capture, which holds the prompt's own
		// ```relevo skeleton, truncated by the capture. The tail contract is
		// for the file the builder writes.
		tail, ok, reject = reporttail.ParseWithReason(body)
	}
	if ok {
		outcome = tail.Status
	} else {
		if reject != "" {
			// A fence was present but unreadable: keep unstructured, but say why
			// so the mastermind does not treat this as "the builder omitted the block".
			note = joinNotes(note, reject)
		}
		// A remote reader's output has its relevo block stripped at the
		// server's close, so parsing cannot recover the status: the view's
		// ReportOutcome is the only record of it (#607 seam 3).
		if fallbackOutcome != "" {
			outcome = fallbackOutcome
		}
	}
	sc := scanForInjection(ctx, rt, "report", b, body)
	note = joinNotes(note, sc.Note)

	// The scope verdict (#801): a refusal forces the outcome to halted and
	// names the path, before the chain event below is built, so a chain
	// member's refusal halts its chain instead of advancing it.
	if verdict.Scoped {
		note = joinNotes(note, scopeNote(verdict))
		if verdict.Refused {
			outcome = reporttail.OutcomeHalted
			tail.HaltedAt = scopeHaltedAt(verdict)
		}
	}

	// The chain event this close produces, built from the in-memory body
	// before the reader output is stripped below (a re-read there would find
	// no block). It names the closing member and the round that closed.
	var closeWF *chainCloseWF
	if chainErr == nil {
		closeWF = &chainCloseWF{Body: body, Path: path, Outcome: outcome, Note: note, Stopped: stopped, Round: closedRound}
	}

	// A chain reader member's saved output has its block stripped before the
	// tail parse above, so that parse reads unstructured while the chain reads
	// the member's real status from its block-carrying message. The member's
	// recorded outcome is that status.
	if closeWF != nil && b.Shape == store.ShapeReader {
		if st := chainReaderCloseOutcome(rt, tx, b, closedRound, body); st != "" {
			outcome = st
		}
	}

	pLines := strings.SplitN(payload, "\n", 2)
	pFirst := pLines[0]
	pRest := ""
	if len(pLines) > 1 {
		pRest = "\n" + pLines[1]
	}

	if outcome != reporttail.OutcomeDone && outcome != reporttail.OutcomeUnstructured {
		// The prefix match accepts the current words and the pre-rename ones,
		// so a payload carrying either is annotated in place; anything else
		// gets the outcome appended.
		prefix := fmt.Sprintf("The runner finished round %d", b.Round)
		legacyPrefix := fmt.Sprintf("Builder finished round %d", b.Round)
		matched := ""
		switch {
		case strings.HasPrefix(pFirst, prefix):
			matched = prefix
		case strings.HasPrefix(pFirst, legacyPrefix):
			matched = legacyPrefix
		}
		if matched != "" {
			replacement := matched + " -- " + outcome
			if tail.HaltedAt != "" {
				replacement += fmt.Sprintf(" at %q", tail.HaltedAt)
			}
			pFirst = replacement + pFirst[len(matched):]
		} else {
			pFirst = pFirst + fmt.Sprintf(" Outcome: %s.", outcome)
		}
	}
	if sc.Flagged > 0 {
		pFirst += flaggedParenthetical(sc.Flagged, sc.Record)
	}
	payload = pFirst + pRest
	if verdict.Refused {
		payload += "\n" + scopePayloadLine(b, verdict)
	}

	closed := ""
	if b.Shape != store.ShapeReader && !HasEntry(entries, b.Round, store.DirToMasterMind, store.KindDiff) {
		d := captureDeps(rt)
		result := capture.RoundDiff(ctx, d, tx, b)
		facts := capture.CommitFacts(ctx, d, b)
		closed = result.EndTree
		diffNote := capture.DiffSummary(result, facts)
		// The key's presence, not the list's, is what makes the counts
		// comparable (#216): changed_paths: [] is a real list of zero paths
		// and must be checked against the diff, while a report with no key
		// at all is never compared.
		pathsMismatch := result.Available && ok && tail.ChangedPathsSet && len(tail.ChangedPaths) != result.Stat.FilesChanged
		if pathsMismatch {
			diffNote = joinNotes(diffNote, fmt.Sprintf("paths: report %d, diff %d", len(tail.ChangedPaths), result.Stat.FilesChanged))
		}
		diffEntry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     b.Round,
			Direction: store.DirToMasterMind,
			Kind:      store.KindDiff,
			Path:      result.Path,
			Note:      diffNote,
			Confirmed: true,
		}
		if facts.Known {
			diffEntry.Commits = facts.Commits
			diffEntry.Tree = "clean"
			if facts.Dirty {
				diffEntry.Tree = "dirty"
			}
		}
		if err := tx.AppendLog(b.Name, diffEntry); err != nil {
			return b, err
		}
		if line := capture.DiffLine(result, facts, b.Branch, b.Name, b.Round); line != "" {
			payload = payload + "\n" + line
		}
		if pathsMismatch {
			payload = payload + "\n" + capture.PathsLine(len(tail.ChangedPaths), result.Stat.FilesChanged)
		}
	}

	// The switches this round took, one line each, after the diff and before
	// the usage the entry carries. A switch is status, not a delivery: nothing
	// is queued for one, so this line is the only trace of it the mastermind
	// gets, and it rides the report that would have been delivered anyway.
	// A round that never switched appends nothing, so its payload is exactly
	// what it was before this existed.
	for _, line := range switchLines(entries, closedRound, payloadNamesSwitches(payload)) {
		payload = payload + "\n" + line
	}

	// The closed round's usage: what the caller measured (a remote
	// binding's server measured its own round and shipped it), or a local
	// read when the caller sent none. entryUsage's type is inferred from
	// the parameter: the parameter's name shadows the usage package in
	// this body, so the type cannot be written out here.
	var entryUsage = usage
	if entryUsage != nil {
		copied := *entryUsage
		entryUsage = &copied
	} else {
		entryUsage = recordUsage(ctx, rt, roundSource(rt, b, roundStart, now))
	}

	// The closed round's cgroup measurement: what the caller already has
	// (a remote binding's server measured its own round and shipped it as
	// view.Rusage), or a local read when the caller sent none -- headless
	// only, since a pane round has no supervisor (#244, #216).
	entryRusage := rusage
	if entryRusage == nil && rt.Runner != nil && b.Builder.Headless() {
		if r, ok := rt.Runner.Rusage(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
			entryRusage = &store.Rusage{CPUMS: r.CPUMS, PeakMemBytes: r.PeakMemBytes}
		}
	}

	var reportPrior = prior
	if reportPrior != nil {
		p := *reportPrior
		reportPrior = &p
	}

	entry := store.LogEntry{
		TS: now, Round: b.Round,
		Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: path, Payload: payload, Note: note,
		Usage:        entryUsage,
		PriorTokens:  reportPrior,
		Rusage:       entryRusage,
		Outcome:      outcome,
		HaltedAt:     tail.HaltedAt,
		ChangedPaths: tail.ChangedPaths,
		CommandsRun:  tail.CommandsRun,
		NotDone:      tail.NotDone,
		Flagged:      sc.Flagged,
		FlaggedBy:    sc.FlaggedBy,
		Classify:     sc.Record,
		Gate:         gate,
	}
	entry.BuilderSession = builderSessionOf(b)
	if chainRunning {
		// A chain member's close is consumed by the chain: the mastermind gets
		// the chain's own end delivery when the chain ends, so the member's
		// report is recorded confirmed with the chain's name rather than
		// queued. delivery.Queue would force Confirmed=false.
		entry.Confirmed = true
		entry.Note = joinNotes(entry.Note, "consumed by chain "+chainRow.Name)
		if err := tx.AppendLog(b.Name, entry); err != nil {
			return b, err
		}
	} else if err := delivery.Queue(ctx, deliveryDeps(rt), tx, b.Name, entry); err != nil {
		return b, err
	}

	// Strip the relevo block from a reader's output file. The parse above is
	// the only reader of the block, and the file the mastermind reads must not
	// carry it. Stripping here, after the entry is queued, means the outcome
	// survives a close that fails later and is retried on the next tick.
	if b.Shape == store.ShapeReader {
		if stripped := reporttail.StripTail(body); !bytes.Equal(stripped, body) {
			if root, rel, err := readerOutputRoot(rt.Store, b, path); err != nil {
				slog.Warn("reader output not stripped", "binding", b.Name, "round", b.Round, "err", err)
			} else {
				err := replaceReaderOutput(root, rel, stripped)
				_ = root.Close()
				if err != nil {
					slog.Warn("reader output not stripped", "binding", b.Name, "round", b.Round, "err", err)
				}
			}
		}
	}

	if stopRequested {
		// Filed under the round that was stopped: b.Round advances below.
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind,
			Kind: store.KindStop, Note: "stopped/graceful", Confirmed: true,
		}); err != nil {
			return b, err
		}
	}

	b.Round++
	b.State = store.StateActive

	// The new round has not been sent yet, so it has no deadline: leaving the
	// old round's start in place would time the next round out against a clock
	// that started before the mastermind had even seen this report. Send stamps a
	// fresh RoundStartedAt when it hands the round over.
	b.RoundStartedAt = time.Time{}

	// A halt notified for the old round says nothing about the new one, so the
	// next round that goes wrong gets its own single notification. The clear
	// takes the kind with the rest: a kind left from an unreachable episode
	// would answer for a halt the new round never had.
	b = clearHaltFields(b)
	// A switch counted against the old round says nothing about the new one,
	// and neither does an exclusion recorded against it (#191).
	b.RoundSwitches = 0
	b.RoundExcluded = nil
	b.RoundOOMKills = 0
	// The round's verify flag has been acted on by the close (#144): the
	// consult, when there is one, is already running.
	b.RoundVerify = false
	b.RoundTier = ""
	b.RoundCPU = nil
	b.RoundBaselineTree = ""
	b.RoundBaselineHead = ""
	// A remote member's closed tree is the pulled result commit the catch-up
	// recorded -- there is no local worktree to snapshot -- so it keeps that
	// value across the advance. Every non-remote binding keeps today's exact
	// assignment.
	if !b.Builder.Remote() {
		b.RoundClosedTree = closed
	}
	// The stream id was the closed round's; the next round's process
	// announces its own (a pane builder has none) (#147).
	b.Builder.StreamSessionID = ""
	b.GateRun = nil
	// A passing gate clears the repair bookkeeping (#132 part 2): the next
	// failing gate gets a fresh budget and a fresh stall comparison, whatever
	// the previous repair rounds cost.
	if gate != nil && gate.Result == "pass" {
		b.RepairCount = 0
		b.LastGateSig = ""
	}
	// A closed round is over: a stall stamped against it says nothing about
	// the next one (#252), and neither do the progress sample or the stale
	// clock (#135).
	b.StalledSince = time.Time{}
	// A request to stop belonged to the round that just closed (#138): the
	// stop bookkeeping never outlives it.
	b.StopRequestedAt = time.Time{}
	b.StopGraceMS = 0
	b.Progress = nil
	b.ExploringSince = time.Time{}
	b.StaleSince = time.Time{}

	// §3.4: a reader round whose artifact directory is over
	// policy.artifact_max_mb closes as usual -- the summary is written and
	// the report is queued -- but the binding asks for a human, and nothing
	// is deleted: sealRounds holds the round back until the cap is raised.
	//
	// The entry is filed under closedRound, not the round the binding has
	// already advanced to: the cause is this round's artifact size, and a
	// default wait still sitting on this round would never pull an N+1 entry
	// (closedRoundHalt).
	//
	// A halt whose entry cannot be written is owed to a later tick, not
	// returned: the report entry above is already on disk whatever this
	// returns, so returning the error would leave the tick unsaved with the
	// binding still at N -- and the next tick would find the round closed,
	// take the idle branch, never halt again, and refuse every send with
	// ErrReportPending.
	if b.Shape == store.ShapeReader {
		if over, total := artifactCapExceeded(rt.Store, b, closedRound, rt.Policy.ArtifactMaxBytes()); over {
			b = closeHaltOrOwe(ctx, rt, tx, b, closedRound, artifactCapReason(total, rt.Policy.ArtifactMaxBytes()))
		}
	}

	// A scope refusal asks for a human, exactly where the reader artifact
	// cap does: after the round has advanced, naming the offending file
	// (#801). Filed under closedRound, and its failed queue owed, for the
	// reasons the cap halt above gives.
	if verdict.Refused {
		b = closeHaltOrOwe(ctx, rt, tx, b, closedRound, scopeHaltText(b, closedRound, verdict))
	}

	// The round has advanced, so the chain may now move: the close is mapped
	// to an event, the pure transition decides, and the action (seed the next
	// member, halt, stop, finish) runs on the chain's own sender in this same
	// critical section. A close that did not advance the chain writes no
	// trace row.
	if closeWF != nil {
		// chainApply returns the binding its action wrote: a remote member's
		// staged repair carries the chain's own bookkeeping (RepairCount,
		// LastGateSig, the staged round's baseline head), and the caller saves
		// what it returns.
		next, cerr := chainApply(ctx, rt, tx, b, closeWF)
		b = next
		if cerr != nil {
			return b, cerr
		}
	}

	return b, nil
}

// deliverAndSettle attempts any pending delivery. The binding is left pending
// when no route can take the payload -- DeliverPending records why, and
// `relevo wait` or the channel's own poll delivers it later (#303 §5.4).
func deliverAndSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.Owner != "" {
		// Owned by a remote client: there is no mastermind. Payloads stay
		// queued; the owner reads them over the wire (remote-builders spec §6.2).
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
