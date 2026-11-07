package workflow

import (
	"maps"
	"slices"
)

// The repair arms a definition's red edges name: the steps a failed check would
// spend a budget on. They are read here, once, so the halt that buys no round
// and the resume that buys one look at the same edges.

// RedArmChecks returns the check steps whose red edge names id, in step order:
// the checks whose failure routes into id, which is what makes id a repair arm.
// It is a pure read of the definition, so a caller outside the engine -- the
// chain resume that reruns the check under a new gate -- can ask the same
// question the halt does without entering the step.
func RedArmChecks(def Definition, id string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(def.Steps)) {
		if redEdgeNames(def.Steps[name], id) {
			out = append(out, name)
		}
	}
	return out
}

// GatingRedCheck names the check gating a pass at the halted state's
// standing, or "" when no single red check gates it. It reuses gatingRedCheck
// on the state the workflow already holds: the halt kept At at the passed
// step with the check's red still in Results and no override recorded, since
// an overridden close never halts. The halted close itself must have taken a
// pass edge, read from the standing step's recorded close: any other halt on
// the same standing keeps today's routing.
func GatingRedCheck(def Definition, st State) string {
	if st.Status != StatusHalted {
		return ""
	}
	at := st.At
	if at == "" {
		return ""
	}
	if !haltClosedOnPass(def, st, at) {
		return ""
	}
	gate, ok := gatingRedCheck(def, st, at)
	if !ok {
		return ""
	}
	if _, ok := gatingRedCheck(def, withoutRed(st, gate), at); ok {
		return ""
	}
	return gate
}

// haltClosedOnPass reports whether the halted close took a pass edge: the
// standing step's recorded close matched a pass arm. A halt whose close took
// any other edge, or recorded none, is not a pass-while-red halt.
func haltClosedOnPass(def Definition, st State, at string) bool {
	last, ok := st.Results[at]
	if !ok {
		return false
	}
	edge, _, matched := matchRun(def.Steps[at].On, last)
	return matched && passEdge(edge)
}

// withoutRed answers one gate in a copy of the state, so a second call to
// gatingRedCheck reports whether another check still gates: only an
// unambiguous gate reroutes.
func withoutRed(st State, gate string) State {
	out := st
	out.Results = make(map[string]Result, len(st.Results))
	for id, r := range st.Results {
		out.Results[id] = r
	}
	if r, ok := out.Results[gate]; ok {
		r.Status = "green"
		out.Results[gate] = r
	}
	return out
}

func redEdgeNames(step Step, id string) bool {
	if step.Check == "" {
		return false
	}
	for _, edge := range []string{"red", "result=red"} {
		if t, ok := step.On[edge]; ok && t.Kind == TargetStep && t.Step == id {
			return true
		}
	}
	return false
}

// IsZeroBudgetArm reports whether id is the zero-max repair arm applyBudget
// halts on rather than routing past.
func IsZeroBudgetArm(def Definition, id string) bool {
	b := def.Steps[id].Budget
	if b == nil || b.Then.Kind != TargetStep || renderLimit(def, b.Max) >= 1 {
		return false
	}
	return len(RedArmChecks(def, id)) > 0
}
