package workflow

import (
	"fmt"
	"strconv"
)

// Start begins a run: it seeds every for-each that walks the plans input and
// enters the start step.
func Start(def Definition, in StartInputs) (State, []Action) {
	s := State{
		Status:  StatusRunning,
		Visits:  map[string]int{},
		Iter:    map[string]Iter{},
		Results: map[string]Result{},
	}
	return enter(def, seedPlans(def, s, in), def.Start, 0)
}

// seedPlans gives every for-each and fork over the plans input its items, with
// the index before the first one.
func seedPlans(def Definition, s State, in StartInputs) State {
	for _, id := range sortedKeys(def.Steps) {
		step := def.Steps[id]
		if step.ForEach == "plans" {
			s.Iter[id] = Iter{Index: -1, Items: append([]string(nil), in.Plans...)}
		}
		if step.Fork != nil && step.Fork.Each == "plans" {
			s.Iter[id] = Iter{Index: -1, Items: append([]string(nil), in.Plans...)}
		}
	}
	return s
}

// enter runs one step: it resets the budgets a step entry resets, checks its
// own budget, then acts by kind. Every entry that routes on without emitting
// an action -- a control step, a budget redirect or an empty check -- consumes
// one unit of the walk; walked counts those entries this transition has made.
func enter(def Definition, s State, id string, walked int) (State, []Action) {
	step, ok := def.Steps[id]
	if !ok {
		return halt(s, id, id+": not a step")
	}
	s = ensureMaps(s)
	// A for-each resets the scopes it names only when it advances, so its
	// entry does not reset them; its next edge does, further down.
	if kindOf(step) != "for-each" {
		s = resetVisits(def, s, id)
	}
	if step.Budget != nil {
		var then Target
		if s, then = applyBudget(def, s, id, step.Budget); then.Kind != "" {
			walked++
			if walked > len(def.Steps)+1 {
				return halt(s, id, capReason(def, id))
			}
			return route(def, s, id, then, walked)
		}
	}
	s.At = id
	switch kindOf(step) {
	case "run":
		return sendRun(def, s, id, step)
	case "check":
		return runCheck(def, s, id, step, walked)
	case "for-each":
		return walkForEach(def, s, id, step, walked+1)
	case "when":
		return walkWhen(def, s, id, step, walked+1)
	case "fork":
		return enterFork(def, s, id, step, walked)
	default:
		return halt(s, id, id+": not a step kind")
	}
}

// ensureMaps gives a state the maps a transition writes into.
func ensureMaps(s State) State {
	if s.Visits == nil {
		s.Visits = map[string]int{}
	}
	if s.Iter == nil {
		s.Iter = map[string]Iter{}
	}
	if s.Results == nil {
		s.Results = map[string]Result{}
	}
	return s
}

// resetVisits clears the count of every budget whose per names the step being
// entered, so a fresh scope starts its count again. A per: chain budget never
// resets. A for-each resets only when it advances: its empty edge leaves the
// counts as they were, so the last scope's count survives the walk's end.
func resetVisits(def Definition, s State, id string) State {
	for _, b := range sortedKeys(def.Steps) {
		budget := def.Steps[b].Budget
		if budget == nil || !budget.Per.names(id) {
			continue
		}
		delete(s.Visits, b)
	}
	return s
}

// names reports whether a per resets on the named step. per: chain never
// resets, so only a plain step list can name one.
func (p Per) names(id string) bool {
	return !p.Chain && containsStr(p.Steps, id)
}

// applyBudget counts a step's visit and returns its then target once the count
// passes the rendered max. A repeated-red event counts as over budget, so the
// same failure the last repair already saw buys no second repair. The target is
// zero when the step is still under.
//
// A budget a red check reaches buys no round at all when its rendered max is
// zero, so its then target would be reached with the failure it was meant to fix
// never attempted. In that case the run halts and names the zero budget: a red
// check reaches a human rather than the next step in the graph.
//
// A budget that no check's red edge names is left alone even at zero -- it is
// not standing in for work a red check left undone -- as is a budget whose
// target is done or a halt, which says what happens next without claiming the
// work was done, and a budget with room, which reaches its target only after
// its own rounds have run and re-checked.
func applyBudget(def Definition, s State, id string, b *Budget) (State, Target) {
	s.Visits[id]++
	if renderLimit(def, b.Max) < 1 && redBudgetArm(def, id) && b.Then.Kind == TargetStep {
		return s, HaltTarget(zeroBudgetReason(id, b))
	}
	if s.repeatRed || s.Visits[id] > renderLimit(def, b.Max) {
		return s, b.Then
	}
	return s, Target{}
}

// redBudgetArm reports whether any check step's red edge names id, which is what
// makes id a repair arm: the step a failed check would spend a budget on.
func redBudgetArm(def Definition, id string) bool {
	for _, name := range sortedKeys(def.Steps) {
		step := def.Steps[name]
		if step.Check == "" {
			continue
		}
		for _, edge := range []string{"red", "result=red"} {
			if t, ok := step.On[edge]; ok && t.Kind == TargetStep && t.Step == id {
				return true
			}
		}
	}
	return false
}

// zeroBudgetReason names the step whose budget renders to zero and what the run
// therefore cannot do, so a halt reads as the budget it is rather than as a
// generic stop.
func zeroBudgetReason(id string, b *Budget) string {
	return id + " has no budget left (" + b.Max.Text() + " renders to 0), so the failure it was meant to fix is unaddressed; the run halts rather than continuing past it. Raise the budget, or rerun the check by hand"
}

// renderLimit reads a budget's max, rendering a param reference. A reference
// that does not render to an int reads as zero.
func renderLimit(def Definition, l Limit) int {
	if l.Ref == "" {
		return l.Count
	}
	n, err := strconv.Atoi(RenderParams(def, l.Ref))
	if err != nil {
		return 0
	}
	return n
}

// sendRun asks the caller to send the run step's actor its raw seed. The round
// is left zero for the caller to fill from the binding it sends to.
func sendRun(def Definition, s State, id string, step Step) (State, []Action) {
	actor := RenderParams(def, step.Run)
	s.Awaiting = Awaiting{Step: id, Member: actor}
	return s, []Action{{Kind: ActionSend, Step: id, Actor: actor, Seed: step.Seed}}
}

// runCheck renders the check's command. An empty command is green with no
// action and no log; like a control step it consumes one unit of the walk, so
// a green edge that loops back cannot recurse past the cap.
func runCheck(def Definition, s State, id string, step Step, walked int) (State, []Action) {
	if command := RenderParams(def, step.Check); command != "" {
		s.Awaiting = Awaiting{Step: id}
		return s, []Action{{Kind: ActionRunCheck, Step: id, Command: command}}
	}
	walked++
	if walked > len(def.Steps)+1 {
		return halt(s, id, capReason(def, id))
	}
	return controlRoute(def, s, id, step.On, "green", walked)
}

// walkForEach advances a for-each and routes next or empty. Past the end the
// index returns to -1, so an input source replays and an artifact source is
// re-read from Results on its next entry.
func walkForEach(def Definition, s State, id string, step Step, walked int) (State, []Action) {
	if walked > len(def.Steps)+1 {
		return halt(s, id, capReason(def, id))
	}
	it := forEachIter(def, s, id, step)
	it.Index++
	if it.Index < len(it.Items) {
		it.Done = false
		s.Iter[id] = it
		s = resetVisits(def, s, id)
		return controlRoute(def, s, id, step.On, "next", walked)
	}
	it.Index = -1
	it.Done = true
	s.Iter[id] = it
	return controlRoute(def, s, id, step.On, "empty", walked)
}

// forEachIter returns the walk a for-each step is on. An artifact source is
// re-read from Results on every entry, so it sees what the earlier step wrote.
func forEachIter(def Definition, s State, id string, step Step) Iter {
	it := s.Iter[id]
	if step.ForEach == "plans" {
		return it
	}
	root, attr, ok := artifactSource(step.ForEach)
	if !ok {
		return it
	}
	if r, ok := s.Results[root]; ok {
		it.Items = r.Artifacts[attr]
	}
	return it
}

// artifactSource reads a for-each source that is exactly one artifact
// reference.
func artifactSource(source string) (string, string, bool) {
	if !IsSingleRef(source) {
		return "", "", false
	}
	refs, err := Refs(source)
	if err != nil || len(refs) != 1 {
		return "", "", false
	}
	return refs[0].Root, refs[0].Attr, true
}

// walkWhen routes a when step on its bool param.
func walkWhen(def Definition, s State, id string, step Step, walked int) (State, []Action) {
	if walked > len(def.Steps)+1 {
		return halt(s, id, capReason(def, id))
	}
	key := "false"
	if whenTrue(def, step.When) {
		key = "true"
	}
	return controlRoute(def, s, id, step.On, key, walked)
}

// whenTrue reads the bool a when names; anything else reads as false.
func whenTrue(def Definition, when string) bool {
	refs, err := Refs(when)
	if err != nil || len(refs) != 1 || refs[0].Root != "params" {
		return false
	}
	p, ok := def.Params[refs[0].Attr]
	return ok && p.Kind == ParamBool && p.Bool
}

// controlRoute routes a control step's outcome, falling back to else, and
// halts when neither matches.
func controlRoute(def Definition, s State, id string, on map[string]Target, key string, walked int) (State, []Action) {
	t, ok := on[key]
	if !ok {
		t, ok = on["else"]
	}
	if !ok {
		return halt(s, id, id+" "+key+": no edge matches")
	}
	return route(def, s, id, t, walked)
}

// capReason names the step a control walk stopped at and the cap it passed.
func capReason(def Definition, id string) string {
	return fmt.Sprintf("control walk exceeded %d steps at %s", len(def.Steps)+1, id)
}
