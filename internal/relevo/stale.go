package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// staleBuilder reports whether b's builder token names a candidate the configured set no longer holds:
// the candidate was edited or deleted after the binding picked it.
func staleBuilder(rt Runtime, b store.Binding) bool {
	if b.BuilderCandidate == "" || b.Builder.Remote() || rt.Candidates == nil {
		return false
	}
	_, ok := rt.Candidates.NameFor(b.BuilderCandidate)
	return !ok
}

// Why a binding was re-picked before its round.
const (
	repickStaleToken = "stale"
	repickWindow     = "window"
)

// windowConfigured reports whether any candidate serving b's role carries a
// window in config: the scope that keeps a host with no window from ever moving
// a binding between rounds.
func windowConfigured(rt Runtime, b store.Binding) bool {
	if rt.Candidates == nil {
		return false
	}
	for _, e := range rankedRole(rt.RoleRegistry(), rt.Candidates, bindingRole(b)) {
		if e.Candidate.Windows != nil {
			return true
		}
	}
	return false
}

// repickForRound picks b's builder again by its actor's order, with an empty
// token over availability.Gates(AvailabilityDeps(rt)), then applyBuilder. It does
// so when b's token is stale, or, when the round names no candidate (named) and
// b is not pinned, when a window is configured and the pick now differs. It
// returns b unchanged, a nil *Resolution and "" otherwise; the string is the
// reason, repickStaleToken or repickWindow. A window re-pick that cannot resolve
// leaves b where it is.
func repickForRound(rt Runtime, b store.Binding, named, allowYolo bool) (store.Binding, *Resolution, string, error) {
	old := b.BuilderCandidate
	stale := staleBuilder(rt, b)
	if !stale && (named || b.BuilderPinned || old == "" || b.Builder.Remote() || !windowConfigured(rt, b)) {
		return b, nil, "", nil
	}

	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), "", bindingRole(b), pickFor(rt))
	if err == nil && !stale && res.Token() == old {
		return b, nil, "", nil
	}
	if err != nil {
		if !stale {
			return b, nil, "", nil
		}
		return b, nil, "", fmt.Errorf("builder %s is no longer configured and no other candidate can take it: %w", old, err)
	}

	next, err := applyBuilder(b, res, rt.RoleRegistry(), rt.Policy, allowYolo)
	if err != nil {
		if !stale {
			return b, nil, "", nil
		}
		return b, nil, "", fmt.Errorf("builder %s is no longer configured and no other candidate can take it: %w", old, err)
	}
	if stale {
		return next, &res, repickStaleToken, nil
	}
	return next, &res, repickWindow, nil
}

// repickLine is the printed reason for a re-pick that left from.
func repickLine(rt Runtime, from, why string, res Resolution) string {
	pick := PickText("builder", res, rt.Candidates)
	if why == repickStaleToken {
		return "note: builder " + from + " is no longer configured; " + pick
	}
	name := rt.Candidates.NameOf(from)
	for _, g := range availability.Gates(AvailabilityDeps(rt)) {
		if g.Token == from && g.Kind == availability.Scheduled && !g.Demote {
			return "note: builder " + name + " is " + g.Note + "; " + pick
		}
	}
	return "note: builder " + name + " yields to a window; " + pick
}
