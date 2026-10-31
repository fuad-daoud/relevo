package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

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
		// The fields are written on a copy the entry is queued against, and only
		// adopted once the queue succeeds: the stamp is what says this halt was
		// told, so a binding whose append failed has to read as untold or the
		// next attempt dedupes against an entry that does not exist. The callers
		// that go on after such a failure stamp it themselves -- closeHaltOrOwe,
		// which owns the owed entry, is the one.
		halted := stampHalt(b, text, kind, rt.Now().UTC())

		slog.Info("binding halted", "binding", b.Name, "round", b.Round, "reason", message)

		if err := queueHalt(ctx, rt, tx, halted, entryRound, text); err != nil {
			return b, err
		}

		halted.HaltNotifiedRound = halted.Round
		b = halted
	}

	b.State = store.StateNeedsYou

	return b, nil
}

// stampHalt writes the fields a halt carries -- the reason a human reads, when
// it began, the kind naming its episode and the state word -- on a copy of b.
//
// HaltAt moves only when the reason changed or none was recorded: a repeat halt
// of the same text is the same halt, and its start is when it first began.
func stampHalt(b store.Binding, text, kind string, now time.Time) store.Binding {
	if b.Halt != text || b.HaltAt.IsZero() {
		b.HaltAt = now.UTC()
	}
	b.Halt = text
	b.RemoteHaltKind = kind
	b.State = store.StateNeedsYou
	return b
}

// ClearHaltFields drops the halt a binding carries, every field of it: the
// reason a human reads, the time it began, the per-round notification key and
// the kind naming the episode. Clearing a halt means clearing all four, because
// each answers a question the binding no longer has. A kind left behind names
// an episode for a halt that is gone: the next answering view then clears a
// halt nothing wrote. A key left behind silences the next halt of the round it
// still names.
func ClearHaltFields(b store.Binding) store.Binding {
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
// HaltNotifiedRound: re-entering haltBinding would queue nothing once the key
// below is stamped, so the marker is what a later tick reads to know the
// notification is still outstanding.
func closeHaltOrOwe(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, closedRound int, message string) store.Binding {
	next, err := closedRoundHalt(ctx, rt, tx, b, closedRound, message)
	if err == nil {
		return next
	}
	slog.Warn("halt entry owed to a later tick", "binding", b.Name, "round", closedRound, "err", err)
	// haltBinding writes nothing when the queue fails, on the understanding that
	// its caller saves nothing. This caller goes on and saves, so the halt is
	// finished here: the fields the entry would have carried are stamped, and
	// with them the key -- which is what keeps the owed entry from being queued
	// twice by a halt of the same round arriving before the retry.
	text := strings.TrimPrefix(message, next.Name+": ")
	next = stampHalt(next, text, "", rt.Now().UTC())
	next.HaltNotifiedRound = next.Round
	next.OwedHalt = &store.OwedHalt{Round: closedRound, Text: text}
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
	if remote.BindingSwitchable(b) {
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
