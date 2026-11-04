package workflow

import "strings"

// PruneEmptyCleanup drops the cleanup steps whose rendered command is empty,
// retargeting their incoming edges to the dropped step's done target, and
// drops the cleanup_ params when no cleanup command remains. A chain with no
// cleanup then stores the shape an old binary reads, while a chain with a
// cleanup keeps the new keys an old binary refuses as unknown.
func PruneEmptyCleanup(def Definition) Definition {
	dropped := map[string]Target{}
	keepCleanup := false
	for id, step := range def.Steps {
		if kindOf(step) != "cleanup" {
			continue
		}
		if RenderParams(def, step.Cleanup) != "" {
			keepCleanup = true
			continue
		}
		dropped[id] = cleanupDoneTarget(step)
	}
	dropParams := !keepCleanup && hasCleanupPrefixParam(def)
	if len(dropped) == 0 && !dropParams {
		return def
	}
	out := def
	out.Steps = make(map[string]Step, len(def.Steps))
	for id, step := range def.Steps {
		if _, ok := dropped[id]; ok {
			continue
		}
		step.On = retargetOn(step.On, dropped)
		if step.Budget != nil {
			budget := *step.Budget
			budget.Then = resolveDroppedTarget(budget.Then, dropped)
			step.Budget = &budget
		}
		out.Steps[id] = step
	}
	if _, ok := dropped[def.Start]; ok {
		if t := resolveDroppedTarget(StepTarget(def.Start), dropped); t.Kind == TargetStep {
			out.Start = t.Step
		}
	}
	if dropParams {
		kept := make(map[string]Param, len(def.Params))
		for name, p := range def.Params {
			if !strings.HasPrefix(name, "cleanup_") {
				kept[name] = p
			}
		}
		out.Params = kept
	}
	return out
}

// cleanupDoneTarget is the edge an empty cleanup's callers take: its done
// edge, or its else when done is absent.
func cleanupDoneTarget(step Step) Target {
	if t, ok := step.On["done"]; ok {
		return t
	}
	if t, ok := step.On["else"]; ok {
		return t
	}
	return DoneTarget()
}

// retargetOn rewrites the step targets a drop removed to the target the
// dropped step named, following chains of dropped steps to the end.
func retargetOn(on map[string]Target, dropped map[string]Target) map[string]Target {
	if on == nil {
		return nil
	}
	out := make(map[string]Target, len(on))
	for key, t := range on {
		out[key] = resolveDroppedTarget(t, dropped)
	}
	return out
}

func resolveDroppedTarget(t Target, dropped map[string]Target) Target {
	seen := map[string]bool{}
	for t.Kind == TargetStep {
		next, ok := dropped[t.Step]
		if !ok || seen[t.Step] {
			return t
		}
		seen[t.Step] = true
		t = next
	}
	return t
}

func hasCleanupPrefixParam(def Definition) bool {
	for name := range def.Params {
		if strings.HasPrefix(name, "cleanup_") {
			return true
		}
	}
	return false
}
