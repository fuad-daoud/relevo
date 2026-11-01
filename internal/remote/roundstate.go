package remote

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// An un-acked close outranks later state because the client fetches catch-up
// by this word.
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

// The daemon brings a switchable builder back by itself, which is the only
// thing that excuses a broken binding from owing its MasterMind an entry. A
// mid-round switch whose spawn failed looks switchable while having no
// process, and no tick retries it, so with no process it is not switchable:
// the failed resolve already gated the bad candidate, so self-healing holds.
func BindingSwitchable(b store.Binding) bool {
	return b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero() && b.Builder.PID != 0
}
