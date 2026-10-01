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

// seedPlans gives every for-each over the plans input its items, with the
// index before the first one.
func seedPlans(def Definition, s State, in StartInputs) State {
	for _, id := range sortedKeys(def.Steps) {
		if def.Steps[id].ForEach == "plans" {
			s.Iter[id] = Iter{Index: -1, Items: append([]string(nil), in.Plans...)}
		}
	}
	return s
}

// enter runs one step: it resets the budgets a step entry resets, checks its
// own budget, then acts by kind. A control step routes on and continues the
// walk; walked counts the control steps this transition has entered.
func enter(def Definition, s State, id string, walked int) (State, []Action) {
	step, ok := def.Steps[id]
	if !ok {
		return halt(s, id, id+": not a step")
	}
	s = ensureMaps(s)
	s = resetVisits(def, s, id)
	if step.Budget != nil {
		var then Target
		if s, then = applyBudget(def, s, id, step.Budget); then.Kind != "" {
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
		return halt(s, id, "fork steps are not run by this engine")
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
// resets.
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
// passes the rendered max. The target is zero when the step is still under.
func applyBudget(def Definition, s State, id string, b *Budget) (State, Target) {
	s.Visits[id]++
	if s.Visits[id] > renderLimit(def, b.Max) {
		return s, b.Then
	}
	return s, Target{}
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
// action and no log: the walk continues straight to the green target.
func runCheck(def Definition, s State, id string, step Step, walked int) (State, []Action) {
	if command := RenderParams(def, step.Check); command != "" {
		s.Awaiting = Awaiting{Step: id}
		return s, []Action{{Kind: ActionRunCheck, Step: id, Command: command}}
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
		s.Iter[id] = it
		return controlRoute(def, s, id, step.On, "next", walked)
	}
	it.Index = -1
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
