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

// redEdgeNames reports whether a check step's red edge routes to id.
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

// IsZeroBudgetArm reports whether id is a repair arm a red check reaches whose
// budget renders to zero: the arm applyBudget halts on rather than routing past.
// A budget no red edge reaches, one with a round left, and one whose then
// target is not a step are all not this halt.
func IsZeroBudgetArm(def Definition, id string) bool {
	b := def.Steps[id].Budget
	if b == nil || b.Then.Kind != TargetStep || renderLimit(def, b.Max) >= 1 {
		return false
	}
	return len(RedArmChecks(def, id)) > 0
}
