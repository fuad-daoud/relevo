package workflow

import "sort"

// stepEdges returns each step's step-targets: its on targets and its budget
// then. done and halt targets are sinks and carry no edge.
func stepEdges(def Definition) map[string][]string {
	edges := make(map[string][]string, len(def.Steps))
	for id := range def.Steps {
		step := def.Steps[id]
		for _, key := range sortedKeys(step.On) {
			if t := step.On[key]; t.Kind == TargetStep {
				edges[id] = append(edges[id], t.Step)
			}
		}
		if step.Budget != nil && step.Budget.Then.Kind == TargetStep {
			edges[id] = append(edges[id], step.Budget.Then.Step)
		}
		sort.Strings(edges[id])
	}
	return edges
}

// kindOf returns a step's single kind, or empty when it does not have exactly
// one. A step with two kinds is reported by rule 2, not read as either.
func kindOf(s Step) string {
	kinds := s.Kinds()
	if len(kinds) != 1 {
		return ""
	}
	return kinds[0]
}

// reachable returns the steps reachable from start, or nil when start is not a
// step.
func reachable(def Definition, start string) map[string]bool {
	if _, ok := def.Steps[start]; !ok {
		return nil
	}
	return reachableFrom(stepEdges(def), start)
}

// reachableFrom walks the edges from start and returns every node it reaches.
func reachableFrom(edges map[string][]string, start string) map[string]bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, m := range edges[n] {
			if !seen[m] {
				seen[m] = true
				queue = append(queue, m)
			}
		}
	}
	return seen
}

// subEdges keeps only the edges whose target is in keep.
func subEdges(edges map[string][]string, keep map[string]bool) map[string][]string {
	out := make(map[string][]string, len(keep))
	for n := range keep {
		for _, m := range edges[n] {
			if keep[m] {
				out[n] = append(out[n], m)
			}
		}
	}
	return out
}

// dominators returns, for every reachable step, the set of steps that dominate
// it: every path from start to the step passes through them. The computation is
// iterative and starts from start alone. It returns nil when start is not a
// step.
func dominators(def Definition, start string) map[string]map[string]bool {
	reach := reachable(def, start)
	if reach == nil {
		return nil
	}
	preds := predecessors(stepEdges(def), reach)
	dom := seedDominators(reach, start)
	for changed := true; changed; {
		changed = false
		for _, n := range sortedKeys(reach) {
			if n == start {
				continue
			}
			next := intersectPreds(dom, preds[n])
			next[n] = true
			if !sameSet(next, dom[n]) {
				dom[n] = next
				changed = true
			}
		}
	}
	return dom
}

// predecessors returns, for every reachable node, the reachable nodes with an
// edge into it.
func predecessors(edges map[string][]string, reach map[string]bool) map[string][]string {
	preds := make(map[string][]string, len(reach))
	for n := range reach {
		for _, m := range edges[n] {
			if reach[m] {
				preds[m] = append(preds[m], n)
			}
		}
	}
	return preds
}

// seedDominators starts the iteration: start dominates only itself, and every
// other reachable node starts with every reachable node.
func seedDominators(reach map[string]bool, start string) map[string]map[string]bool {
	all := make(map[string]bool, len(reach))
	for m := range reach {
		all[m] = true
	}
	dom := make(map[string]map[string]bool, len(reach))
	for n := range reach {
		if n == start {
			dom[n] = map[string]bool{start: true}
			continue
		}
		dom[n] = copySet(all)
	}
	return dom
}

// intersectPreds intersects the dominator sets of a node's predecessors, the
// middle step of the dominator equation.
func intersectPreds(dom map[string]map[string]bool, preds []string) map[string]bool {
	if len(preds) == 0 {
		return map[string]bool{}
	}
	next := copySet(dom[preds[0]])
	for _, p := range preds[1:] {
		intersect(next, dom[p])
	}
	return next
}

// dominates reports whether a strictly dominates b: every path to b passes
// through a, and a is not b.
func dominates(dom map[string]map[string]bool, a, b string) bool {
	if a == b {
		return false
	}
	set, ok := dom[b]
	return ok && set[a]
}

func copySet(s map[string]bool) map[string]bool {
	out := make(map[string]bool, len(s))
	for k := range s {
		out[k] = true
	}
	return out
}

func intersect(a, b map[string]bool) {
	for k := range a {
		if !b[k] {
			delete(a, k)
		}
	}
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// graphCycle returns the steps of one directed cycle within the subgraph
// induced by keep, sorted, or nil when the subgraph has none. It visits nodes
// and edges in sorted order, so the cycle it finds is deterministic.
func graphCycle(edges map[string][]string, keep map[string]bool) []string {
	const (
		white = iota
		gray
		black
	)
	color := make(map[string]int, len(keep))
	var stack, found []string
	var dfs func(string) bool
	dfs = func(n string) bool {
		color[n] = gray
		stack = append(stack, n)
		for _, m := range edges[n] {
			if !keep[m] {
				continue
			}
			switch color[m] {
			case white:
				if dfs(m) {
					return true
				}
			case gray:
				start := 0
				for i, s := range stack {
					if s == m {
						start = i
						break
					}
				}
				found = append([]string(nil), stack[start:]...)
				sort.Strings(found)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, n := range sortedKeys(keep) {
		if color[n] == white && dfs(n) {
			return found
		}
	}
	return nil
}

// cycle is one rule 5 failure: the step to report and what is wrong.
type cycle struct {
	Step   string
	Detail string
}

// boundedCycles returns the cycles rule 5 rejects. A cycle is bounded when it
// passes through a for-each step, or through a budgeted step whose per steps
// lie outside the cycle. A cycle of control steps alone is always rejected.
func boundedCycles(def Definition) []cycle {
	edges := stepEdges(def)
	control := make(map[string]bool)
	nonRunCheck := make(map[string]bool)
	bounded := make(map[string]bool)
	for id, step := range def.Steps {
		switch kindOf(step) {
		case "run", "check":
		case "for-each":
			control[id] = true
			bounded[id] = true
		case "when":
			control[id] = true
		default:
			nonRunCheck[id] = true
		}
		if step.Budget != nil {
			bounded[id] = true
		}
	}

	var out []cycle
	if c := graphCycle(edges, control); c != nil {
		out = append(out, cycle{c[0], "a cycle made only of control steps"})
	} else if c := graphCycle(edges, nonRunCheck); c != nil {
		out = append(out, cycle{c[0], "a cycle with no run or check step"})
	}

	unbounded := make(map[string]bool)
	for id := range def.Steps {
		if !bounded[id] {
			unbounded[id] = true
		}
	}
	if c := graphCycle(edges, unbounded); c != nil {
		out = append(out, cycle{c[0], "an unbounded cycle: no for-each and no budget"})
	}

	for _, id := range sortedKeys(def.Steps) {
		step := def.Steps[id]
		if step.Budget == nil || step.Budget.Per.Chain {
			continue
		}
		keep := make(map[string]bool, len(def.Steps))
		for n := range def.Steps {
			if bounded[n] && n != id {
				continue
			}
			keep[n] = true
		}
		sub := subEdges(edges, keep)
		for _, per := range step.Budget.Per.Steps {
			if !keep[per] {
				continue
			}
			if reachableFrom(sub, id)[per] && reachableFrom(sub, per)[id] {
				out = append(out, cycle{id, "a budget whose per resets inside its own cycle"})
				break
			}
		}
	}
	return out
}
