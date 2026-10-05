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
		// The kind is stamped before the halt, not after it returns: haltAndSettle
		// reads the binding it is handed, so a kind set on its result would never
		// reach disk.
		b.RemoteHaltKind = store.HaltKindUnreachable
		return haltAndSettle(ctx, rt, tx, b, name+": "+unreachableHaltText(server, dur, b.Round))
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

// unreachableHalted reports whether this binding is NEEDS YOU because the server
// could not be reached, which is to say whether RemoteHaltKind names the
// unreachable episode.
//
// The field, not the text: a halt reaching this binding from the server carries
// whatever reason the server sent, and a builder's raw failure lines can contain
// this episode's marker by coincidence. Every other halt on a remote binding --
// a removed binding, a revoked key, a clock skewed past the auth grace, the
// server's own view.Halt -- names no kind at all, so each of them is left alone
// here for the same reason and by the same test.
//
// The migration in store's decode is what keeps an episode recorded before the
// field existed recognisable: an older record's halt text is read there, once.
func unreachableHalted(b store.Binding) bool {
	return b.RemoteHaltKind == store.HaltKindUnreachable
}

// clearUnreachableHalt drops the unreachable halt once the server answers again:
// a round that is visibly running or queued disproves the halt's reason outright,
// and a needs-you view supersedes it, because the halt it carries is the
// server's own statement about the round.
//
// The running and queued answer is the negative one -- the round is alive over
// there, so nothing about it needs a human. Needs-you needs the positive one:
// the server is answering, it has looked at the round, and it has said a human
// is needed with a reason of its own. Left in place, that reason would never be
// told: the unreachable episode already stamped HaltNotifiedRound for this
// round, so the server's halt is deduped away and the binding sits on NEEDS YOU
// quoting a halt the server has just contradicted.
//
// The halt's only reason was "we cannot see the server", and a view that came
// back disproves exactly that. Leaving the binding on NEEDS YOU until the server
// closes the round makes `wait` answer needs-you on a round a human can see
// progressing.
//
// The stamp is cleared with the text, not left behind, so a server that drops out
// again is a new episode: its halt finds HaltNotifiedRound back at zero and
// queues its own entry rather than staying silent for a round already notified.
//
// Two halts are answered by a view rather than by a path of their own: the
// unreachable one, which the kind names, and the broken-round halt this binding
// was given by the server saying its builder was gone. Every other halt -- a 404,
// a revoked key, a clock skewed past the auth grace, a local halt -- says
// something no arriving view has contradicted, and each is answered by its own
// path. The cleared state is Active, matching what a binding with no halt at all
// carries.
//
// A view whose own reason is a halt is not among them: a needs-you view that
// carries one is answered by the server's halt replacing the unreachable one,
// because the unreachable episode already stamped HaltNotifiedRound for this
// round and the server's own reason would otherwise be deduped away. The same
// stamp is why a needs-you view does not clear a broken halt here: the reason it
// carries is the account of what the round needs, and it belongs on the binding
// in place of the break rather than under it.
func clearUnreachableHalt(state remote.RoundState, b store.Binding) store.Binding {
	if !unreachableHaltDisproven(state) {
		return b
	}
	if b.State != store.StateNeedsYou {
		return b
	}
	if !unreachableHalted(b) && !brokenHaltedOnRunning(b, state) {
		return b
	}
	slog.Info("remote halt cleared: the server is answering again",
		"binding", b.Name, "round", b.Round, "reason", b.Halt)
	b.Halt = ""
	b.HaltAt = time.Time{}
	b.HaltNotifiedRound = 0
	// The kind goes with the text it named: a binding carrying it with no halt
	// would answer unreachableHalted for a halt it no longer has, and the next
	// unreachable arm would clear nothing while the field said otherwise.
	b.RemoteHaltKind = ""
	b.State = store.StateActive
	return b
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
	case remote.RoundRunning, remote.RoundQueued, remote.RoundNeedsYou:
		return true
	}
	return false
}

// brokenHaltText is the reason a broken-round halt gets when the server named no
// reason of its own. It is a constant of this file rather than a builder of
// arbitrary text, so it can be compared against b.Halt whole: a broken-round halt
// carrying it is one this file wrote, and nothing a server sends can be mistaken
// for it by quoting a word from inside it.
const brokenHaltText = "the server's builder for this round is gone; rebind before sending"

// brokenHaltedOnRunning reports whether b.Halt is the fallback broken-round halt
// and the arriving view says the round is running again.
//
// The halt's claim was that nothing was running the round, and a running round
// over there is that thing seen again, so the view contradicts it outright. Only
// this one halt: the server's own named reason for a break is its statement
// about the round and stays until the server replaces it, and a 404, a revoked
// key, a clock skewed past the auth grace or a local halt say something no
// running round has contradicted.
//
// Queued does not clear it and needs-you does not either. Queued is the server
// holding a round with no builder behind it, which is the halt's own claim
// uncontradicted; needs-you carries a reason the MasterMind has to be told, and
// needsYou above already stops this for the unreachable halt on the same
// grounds.
func brokenHaltedOnRunning(b store.Binding, state remote.RoundState) bool {
	return b.Halt == brokenHaltText && state == remote.RoundRunning
}
