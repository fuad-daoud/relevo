package remote

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// RoundStateOf returns the execution state of an owned binding.
//
// An un-acked close outranks later state because the client fetches catch-up
// by this word: a needs_you view makes the client skip the fetch, so the
// round would never close.
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

// BindingSwitchable reports whether the daemon will bring a builder back by
// itself, which is the only thing that excuses a broken binding from owing
// its MasterMind an entry. It mirrors view.WaitingOn's own test, and the two
// must agree: a switchable break is not waiting on a human.
//
// The PID clause is what makes that agreement reachable: a round's candidate
// and RoundStartedAt survive the switch that failed to replace them, so a
// mid-round switch whose spawn failed only looks switchable while having no
// process at all. No tick retries it, so with no process it is not
// switchable: the failed resolve already gated the bad candidate, so
// self-healing holds.
func BindingSwitchable(b store.Binding) bool {
	return b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero() && b.Builder.PID != 0
}
