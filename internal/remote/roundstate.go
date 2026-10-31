package remote

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// RoundStateOf returns the execution state of an owned binding.
// It is derived live from the binding and its entries, never stored.
//
// An un-acked close outranks the halt that follows it. A round with a done
// marker closes whatever the earlier state was, so a binding that closed and
// then halted again -- a reader over its artifact cap, a refused scope, a
// resend whose admit failed -- still owes its owner that close. Reporting
// needs_you instead would hide it: the client fetches a catch-up by this word,
// and a needs_you view makes it skip the fetch, so the round would never
// close.
func RoundStateOf(b store.Binding, entries []store.LogEntry) RoundState {
	if b.Serve != nil && b.Serve.ClosedRound > b.Serve.AckedRound {
		return RoundClosed
	}
	if b.State == store.StateNeedsYou {
		return RoundNeedsYou
	}
	// A broken builder has no process to run the round, whatever the entries
	// say about it, so it outranks the open-round arms below -- but not the
	// close above, which the owner is still owed.
	//
	// A switchable break is not that: the server retries it on its next tick
	// and deliberately notifies nobody about it, so reporting it broken makes
	// the owner halt on a fallback reason of its own and queue an entry for a
	// fault the daemon is about to fix. It falls through to the open-round
	// arms, which is what it was before the break.
	if b.State == store.StateBroken && !BindingSwitchable(b) {
		return RoundBroken
	}
	open := store.RoundOpen(entries, b.Round)
	if open && !b.QueuedAt.IsZero() {
		return RoundQueued
	}
	if open {
		return RoundRunning
	}
	return RoundIdle
}

// BindingSwitchable is the one definition of "the daemon will bring a builder
// back by itself", which is the only thing that excuses a broken binding from
// owing its MasterMind an entry. It mirrors view.WaitingOn's own test, and the
// two must agree: WaitingOn answers ok=false for a switchable broken binding
// because the fault resolves itself, and queueBrokenHalt queues for every
// binding WaitingOn does call waiting. view keeps its own deliberate
// duplicate; the engine calls down to this one.
//
// The PID clause is what makes that agreement reachable. A round's builder
// candidate and RoundStartedAt survive the switch that failed to replace them,
// so a mid-round switch whose spawn failed looks switchable while having no
// process at all -- and no tick retries it (reconcileHeadless returns at the
// PID == 0 branch without acting). That binding is waiting on a human, not on
// the daemon, so with no process it is not switchable. The spawn_failed ledger
// entry the failed resolve already recorded still gates the bad candidate, so
// self-healing is preserved: the next human send picks a different one.
func BindingSwitchable(b store.Binding) bool {
	return b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero() && b.Builder.PID != 0
}
