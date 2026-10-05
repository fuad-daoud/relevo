package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The halt text is the reason a human reads, not the episode's identity. A
// server's own view.Halt reaches b.Halt through haltBinding like every other
// halt, and a builder's raw failure lines can quote any phrase -- including this
// one -- so recognising an unreachable halt by its text made the server's halt
// answer to a question only this file may answer, and cleared it on the next
// view. store.Binding.RemoteHaltKind names the episode instead; the text is
// built from the marker and nothing reads it back.

// applyRemoteUnreachable is the unreachable arm of a failed fetch: stamp when the
// outage began, report it as the status, and halt once an open round has been
// unreachable for longer than its budget plus the grace.
//
// It lives beside the halt's own text and its clearing because those three are
// one behaviour: an episode this opens, the sentence it records, and the moment
// the server's answering ends it. Splitting them across two files would put the
// write and the undo next to each other's callers and nowhere near each other.
//
// The halt fires only on an open round: a round the server already closed has
// nothing left running over there, and its answer arrives as a view rather than
// as silence.
func applyRemoteUnreachable(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, now time.Time) (store.Binding, error) {
	server := b.Builder.Server
	name := b.Name
	if b.RemoteUnreachableSince.IsZero() {
		b.RemoteUnreachableSince = now
		slog.Warn(fmt.Sprintf("%s unreachable", server), "server", server, "binding", name)
	}
	b.Builder.RemoteStatus = "unreachable"

	entries, rerr := tx.ReadLog(name)
	roundOpen := rerr == nil && store.RoundOpen(entries, b.Round)

	dur := now.Sub(b.RemoteUnreachableSince)
	if roundOpen && dur > roundBudget(b)+unreachableGrace {
		// The kind rides the halt into the notification guard rather than being
		// stamped here: a halt already notified for this round stamps nothing,
		// and a kind set regardless would name this episode for the reason that
		// did go out.
		return haltAndSettleKind(ctx, rt, tx, b, name+": "+unreachableHaltText(server, dur, b.Round), store.HaltKindUnreachable)
	}
	return b, nil
}

// unreachableHaltText is the halt an unreachable server earns: past the round
// budget and the grace, the round may still be running where nobody can see it,
// so a human has to decide whether to keep waiting or stop it.
//
// The caller passes the name-stripped text to haltAndSettle, which prefixes the
// binding name; b.Halt keeps what this returns.
func unreachableHaltText(server string, dur time.Duration, round int) string {
	return fmt.Sprintf("%s %s %s; round %d may still be running there",
		server, store.UnreachableHaltMarker, dur.Truncate(time.Second), round)
}

// clearUnreachableHalt drops a remote halt once the server answers again. What
// the arriving view has to answer is the claim the halt's own episode made, and
// the kind names that episode, so the kind picks the rule.
//
// A running or queued view answers either claim negatively: the round is alive
// over there, so nothing about it needs a human. The unreachable halt is also
// answered by a needs-you or broken view, because a server that has looked at
// the round and says a human is needed, or that its builder is gone, has
// contradicted the only thing the outage said -- and left in place that reason
// would never be told, because the unreachable episode already stamped
// HaltNotifiedRound for this round.
//
// The broken halt is answered by running or queued and by nothing else: a
// needs-you view's reason is the account of what the round needs, and it belongs
// on the binding in place of the break rather than under it -- so such a view
// halts the binding afresh, through haltBindingKind, and stamps its own kind
// over the break's.
//
// The clear is by the kind alone and never by the text. A server names a reason
// for a broken round when it has one, so matching the fallback text cleared only
// the breaks a server with nothing to say produced; a named reason, which is
// every current server's, stayed until the server replaced it, and `wait`
// answered needs-you on a round a human could watch move.
//
// The stamp is cleared with the halt, so a server that drops out or breaks again
// is a new episode: its halt finds HaltNotifiedRound back at zero and queues its
// own entry rather than staying silent for a round already notified. The cleared
// state is Active, matching what a binding with no halt at all carries.
func clearUnreachableHalt(state remote.RoundState, b store.Binding) store.Binding {
	if b.State != store.StateNeedsYou {
		return b
	}
	if !haltEpisodeAnswered(state, b.RemoteHaltKind) {
		return b
	}
	slog.Info("remote halt cleared: the server is answering again",
		"binding", b.Name, "round", b.Round, "reason", b.Halt)
	b = clearHaltFields(b)
	b.State = store.StateActive
	return b
}

// haltEpisodeAnswered reports whether the arriving view answers the claim the
// halt's episode made. The kind names the episode; a halt that names none was
// not written by a path this file clears, whatever its text happens to be.
func haltEpisodeAnswered(state remote.RoundState, kind string) bool {
	switch kind {
	case store.HaltKindUnreachable:
		return unreachableHaltDisproven(state)
	case store.HaltKindBroken:
		return state == remote.RoundRunning || state == remote.RoundQueued
	}
	return false
}

// haltKindFor names the episode a halting view belongs to. Only a broken view
// names one: every other halting word -- needs-you included -- carries the
// server's own statement about the round, and no view this file writes may clear
// it.
func haltKindFor(state remote.RoundState) string {
	if state == remote.RoundBroken {
		return store.HaltKindBroken
	}
	return ""
}

// remoteHaltText is the reason a halting view halts this binding for.
//
// The server's own text is what a halt is for: it names the switch that failed
// and what it failed with, which is the only account of the break that exists.
// A served binding ships that text for a broken round, so the break arrives
// named -- but not always: a server predating the field omits it, and so does a
// break from a writer with nothing to say. A broken view with no text still owes
// the one entry a halt owes, and haltBinding given an empty one files an entry
// whose payload says nothing at all, so the fallback supplies the reason instead.
//
// Only a broken view is second-guessed. Every other halting word carries a text
// the server means, and second-guessing one would replace a real reason with a
// guess.
//
// The caller passes this to haltAndSettle, which prefixes the binding name and
// strips it back off for b.Halt.
func remoteHaltText(state remote.RoundState, halt string) string {
	if state == remote.RoundBroken && halt == "" {
		return brokenHaltText
	}
	return halt
}

// unreachableHaltDisproven is the set of round states whose arriving view
// answers the unreachable halt's question. Split out so the clearing rules are
// one predicate rather than a condition growing arms inside the function that
// reads it.
func unreachableHaltDisproven(state remote.RoundState) bool {
	switch state {
	case remote.RoundRunning, remote.RoundQueued, remote.RoundNeedsYou, remote.RoundBroken:
		return true
	}
	return false
}

// brokenHaltText is the reason a broken-round halt gets when the server named no
// reason of its own. It is a constant of this file rather than a builder of
// arbitrary text, so a reader of the halt sees the same sentence every time; the
// kind, not this text, is what says the halt came from a broken view, so nothing
// compares a halt against it.
const brokenHaltText = "the server's builder for this round is gone; rebind before sending"
