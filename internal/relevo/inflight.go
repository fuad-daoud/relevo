package relevo

import (
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// LiveBuilder reports whether b holds a slot at its provider: a headless
// builder with a process, on a binding that is not DONE or PAUSED. A queued
// round holds no slot yet and a halted round has no process.
func LiveBuilder(b store.Binding) bool {
	return b.Builder.Headless() && b.Builder.PID != 0 && b.State != store.StateDone && b.State != store.StatePaused
}

// openRoundsByCandidate counts the live builders per candidate token, the
// whole token: an opencode model names its effort ("#xhigh"), and the refs the
// counts are looked up by carry it.
func openRoundsByCandidate(bindings []store.Binding) map[string]int {
	out := map[string]int{}
	for _, b := range bindings {
		if !LiveBuilder(b) {
			continue
		}
		out[b.BuilderCandidate]++
	}
	return out
}

// CountInFlight is openRoundsByCandidate over bindings without the one named
// except.
func CountInFlight(bindings []store.Binding, except string) map[string]int {
	kept := make([]store.Binding, 0, len(bindings))
	for _, b := range bindings {
		if b.Name != except {
			kept = append(kept, b)
		}
	}
	return openRoundsByCandidate(kept)
}

// inFlightOf is rt's count source: its own InFlight, else a walk over its
// store, else none.
func inFlightOf(rt Runtime) func(string) map[string]int {
	if rt.InFlight != nil {
		return rt.InFlight
	}
	if rt.Store == nil {
		return nil
	}
	return func(except string) map[string]int {
		bindings, err := rt.Store.List()
		if err != nil {
			return nil
		}
		return CountInFlight(bindings, except)
	}
}

// switchGates is the gate set a mid-round switch of b resolves over: the live
// gates without b's own round counted against a concurrency cap -- b's slot is
// being handed over, not added to -- plus the candidates this round excluded.
func switchGates(rt Runtime, b store.Binding) []availability.Gate {
	deps := AvailabilityDeps(rt)
	deps.InFlightExcept = b.Name
	return withSkillGates(rt, append(availability.Gates(deps), roundExclusionGates(b)...), b.CWD, nil)
}
