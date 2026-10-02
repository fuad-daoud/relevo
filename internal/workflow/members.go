package workflow

import "sort"

// UsedActors returns the actors a workflow's run steps name, deduped and
// sorted. It walks the graph from start and, at a when step, follows only the
// branch the workflow's own params select, so an actor reached only through a
// branch the params cut is not used. Callers resolve the params first, so the
// names it returns are the ones the run would ask for.
func UsedActors(def Definition) []string {
	used := map[string]bool{}
	seen := map[string]bool{}
	queue := []string{def.Start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		step, ok := def.Steps[id]
		if !ok {
			continue
		}
		if kindOf(step) == "run" {
			used[RenderParams(def, step.Run)] = true
		}
		queue = append(queue, nextSteps(def, step)...)
	}
	out := make([]string, 0, len(used))
	for actor := range used {
		out = append(out, actor)
	}
	sort.Strings(out)
	return out
}

// nextSteps returns the steps one step can enter. A when step carries only the
// branch its param selects, with the same else fallback the run itself takes;
// every other step carries each of its step targets. done and halt targets
// carry none, because they end the walk.
func nextSteps(def Definition, step Step) []string {
	if kindOf(step) == "when" {
		key := "false"
		if whenTrue(def, step.When) {
			key = "true"
		}
		t, ok := step.On[key]
		if !ok {
			t = step.On["else"]
		}
		return stepTarget(t)
	}
	var out []string
	for _, key := range sortedKeys(step.On) {
		out = append(out, stepTarget(step.On[key])...)
	}
	if step.Budget != nil {
		out = append(out, stepTarget(step.Budget.Then)...)
	}
	return out
}

// stepTarget is the step a target names, or nothing when it finishes or halts.
func stepTarget(t Target) []string {
	if t.Kind == TargetStep {
		return []string{t.Step}
	}
	return nil
}
