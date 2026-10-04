package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// unreachableHaltMarker is the phrase every unreachable-past-budget halt text
// carries, and the whole of how one is recognised again later. The text is the
// identity: a binding records which server was unreachable and for how long, and
// nothing else about the episode, so a marker is all the clear path has to
// match on. A dedicated store.Binding field would say the same thing with a
// format bump and a migration for every existing row.
const unreachableHaltMarker = "unreachable for"

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
		server, unreachableHaltMarker, dur.Truncate(time.Second), round)
}

// unreachableHalted reports whether b.Halt is the halt unreachableHaltText
// writes, which is to say whether this binding is NEEDS YOU because the server
// could not be reached.
//
// Recognising the halt by its text is deliberate. Every other halt on a remote
// binding -- a removed binding, a revoked key, a clock skewed past the auth
// grace, the server's own view.Halt -- has a different reason and a different
// owner, so the ones this must leave alone do not match. The builder and this
// predicate share unreachableHaltMarker, so a wording change moves both at once
// and the two cannot drift into disagreeing about which halts clear.
func unreachableHalted(b store.Binding) bool {
	return strings.Contains(b.Halt, unreachableHaltMarker)
}

// clearUnreachableHalt drops the unreachable halt once the server answers again
// with a round that is visibly running or queued.
//
// The halt's only reason was "we cannot see the server", and a running or queued
// view disproves exactly that: the round is alive over there. Leaving the binding
// on NEEDS YOU until the server closes the round makes `wait` answer needs-you
// on a round a human can see progressing.
//
// The stamp is cleared with the text, not left behind, so a server that drops out
// again is a new episode: its halt finds HaltNotifiedRound back at zero and
// queues its own entry rather than staying silent for a round already notified.
//
// Only the unreachable halt is touched. A server view.Halt, a 404, an auth halt
// and a local halt all say something the server has not yet contradicted, and
// each of them is answered by its own path. The cleared state is Active, matching
// what a binding with no halt at all carries.
func clearUnreachableHalt(state remote.RoundState, b store.Binding) store.Binding {
	if state != remote.RoundRunning && state != remote.RoundQueued {
		return b
	}
	if b.State != store.StateNeedsYou || !unreachableHalted(b) {
		return b
	}
	slog.Info("unreachable halt cleared: the server is answering again",
		"binding", b.Name, "round", b.Round, "reason", b.Halt)
	b.Halt = ""
	b.HaltAt = time.Time{}
	b.HaltNotifiedRound = 0
	b.State = store.StateActive
	return b
}
