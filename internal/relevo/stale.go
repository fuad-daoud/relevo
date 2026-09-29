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

// repickStale picks b's builder again by its actor's order when staleBuilder(rt, b): resolveRole with an empty
// token over availability.Gates(AvailabilityDeps(rt)), then applyBuilder. It returns b unchanged and a nil *Resolution when b is not stale.
func repickStale(rt Runtime, b store.Binding, allowYolo bool) (store.Binding, *Resolution, error) {
	if !staleBuilder(rt, b) {
		return b, nil, nil
	}

	old := b.BuilderCandidate
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), "", bindingRole(b))
	if err != nil {
		return b, nil, fmt.Errorf("builder %s is no longer configured and no other candidate can take it: %w", old, err)
	}

	next, err := applyBuilder(b, res, rt.RoleRegistry(), rt.Policy, allowYolo)
	if err != nil {
		return b, nil, fmt.Errorf("builder %s is no longer configured and no other candidate can take it: %w", old, err)
	}
	return next, &res, nil
}
