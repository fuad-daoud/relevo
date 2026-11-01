package workflow

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Next advances a run by one event: a terminal run, or an event that does not
// name the awaited close, is returned unchanged, so a replayed close can never
// advance a run twice.
func Next(def Definition, s State, e Event) (State, []Action) {
	if s.Status.terminal() || !s.awaits(e) {
		return s, nil
	}
	s.repeatRed = e.RepeatRed
	switch e.Kind {
	case EventStepClosed:
		return s.stepClosed(def, e)
	case EventCheckClosed:
		return s.checkClosed(def, e)
	case EventChildEnded:
		return s.childEnded(def, e)
	case EventMergeClosed:
		return s.mergeClosed(def, e)
	case EventNeedsYou:
		return halt(s, e.Step, e.Reason)
	case EventStopped:
		return stop(s)
	default:
		return s, nil
	}
}

func (s State) awaits(e Event) bool {
	switch e.Kind {
	case EventStepClosed:
		return s.Awaiting.matchesRun(e)
	case EventCheckClosed:
		return s.Awaiting.matchesCheck(e)
	case EventChildEnded:
		return s.Awaiting.matchesChild(e)
	case EventMergeClosed:
		return s.Awaiting.matchesMerge(e)
	case EventNeedsYou, EventStopped:
		if s.Awaiting.Member != "" {
			return s.Awaiting.matchesRun(e)
		}
		if len(s.Awaiting.Children) > 0 {
			return e.Step == s.Awaiting.Step
		}
		return s.Awaiting.matchesCheck(e)
	default:
		return false
	}
}

func (a Awaiting) matchesChild(e Event) bool {
	if a.Step != e.Step || a.Merging {
		return false
	}
	if !slices.Contains(a.Children, e.Child) {
		return false
	}
	if a.Ended != nil {
		if _, ended := a.Ended[e.Child]; ended {
			return false
		}
	}
	return true
}

func (a Awaiting) matchesMerge(e Event) bool {
	return a.Merging && a.Step == e.Step
}

func (a Awaiting) matchesRun(e Event) bool {
	return a.Member != "" && e.Step == a.Step && e.Member == a.Member && e.Round == a.Round
}

func (a Awaiting) matchesCheck(e Event) bool {
	return a.Member == "" && len(a.Children) == 0 && e.Step == a.Step && e.Run == a.Run
}

// stepClosed records a run step's close and routes it on. A close that advances
// past a red check carries its override into the action the trace stores, so
// the pass stays reviewable; any other unproven advance halts instead.
func (s State) stepClosed(def Definition, e Event) (State, []Action) {
	s.recordStep(e)
	key, t, ok := matchRun(def.Steps[e.Step].On, s.Results[e.Step])
	if !ok {
		return s.unmatchedRun(e)
	}
	if reason, gated := s.redCheckGate(def, e, key); gated {
		return halt(s, e.Step, reason)
	}
	next, actions := route(def, s, e.Step, t, 0)
	return next, carryOverride(actions, e)
}

// redCheckGate reports the halt reason for a close that takes a pass edge while
// a red check in front of it is still unanswered; a close routing onward for any
// other reason is left alone. An explicit override on the close answers the gate,
// landing in the state, the encoded event and the action a trace row stores.
func (s State) redCheckGate(def Definition, e Event, edge string) (string, bool) {
	if !passEdge(edge) || e.Override != "" {
		return "", false
	}
	gate, ok := gatingRedCheck(def, s, e.Step)
	if !ok {
		return "", false
	}
	return e.Step + " passed while check " + gate + " is red: the run does not pass a red check without an explicit override naming why; rerun the check, or record the override on the close", true
}

// gatingRedCheck names the check whose red still gates a pass at the closing
// step, or ok false when no red stands in its way. Only a check upstream of the
// closing step whose latest result is red, with no recorded override answering
// it, gates; an answered red is stale, so a later pass is gated by its own
// phase's check or by nothing.
func gatingRedCheck(def Definition, s State, at string) (string, bool) {
	edges := stepEdges(def)
	for _, id := range slices.Sorted(maps.Keys(def.Steps)) {
		if def.Steps[id].Check == "" {
			continue
		}
		if !reachableFrom(edges, id)[at] {
			continue
		}
		if r, ok := s.Results[id]; ok && r.Status == "red" && !overrodeGate(s, reachableFrom(edges, id)) {
			return id, true
		}
	}
	return "", false
}

// overrodeGate reports whether some recorded close already answered a red the
// pass stood on: an override belongs to a step downstream of the gate it was
// recorded against, so an override on a step the gate never reaches answers
// nothing here.
func overrodeGate(s State, downstream map[string]bool) bool {
	for id, r := range s.Results {
		if r.Override != "" && downstream[id] {
			return true
		}
	}
	return false
}

// passEdge reports whether an edge name is a pass arm: the edges a close takes
// to say a gate is satisfied.
func passEdge(edge string) bool {
	if edge == passWord {
		return true
	}
	_, value, ok := cutEq(edge)
	return ok && value == passWord
}

const passWord = "pass"

// carryOverride stamps a close's override onto the actions it produced, so the
// encoded action a trace row stores names the override beside the send; the
// reason is set too, so a row rendering only the reason still shows why a red
// gate was passed.
func carryOverride(actions []Action, e Event) []Action {
	if e.Override == "" {
		return actions
	}
	reason := e.Step + " passed a red check on the recorded override " + e.Override
	for i := range actions {
		actions[i].Override = e.Override
		if actions[i].Reason == "" {
			actions[i].Reason = reason
		}
	}
	return actions
}

// unmatchedRun halts a run close that matched no edge, preferring a
// caller-rendered halt reason, then the runner's reason, then the outcomes.
func (s State) unmatchedRun(e Event) (State, []Action) {
	if e.HaltReason != "" {
		return halt(s, e.Step, e.HaltReason)
	}
	if e.Status == "done" {
		return halt(s, e.Step, doneUnmatchedReason(e))
	}
	if e.Reason == "" {
		return halt(s, e.Step, e.Step+" "+e.Status)
	}
	return halt(s, e.Step, e.Step+" "+e.Status+": "+e.Reason)
}

func doneUnmatchedReason(e Event) string {
	if len(e.Outcomes) == 0 {
		return e.Step + " done: no edge matches"
	}
	return e.Step + " done: no edge matches outcomes " + outcomesText(e.Outcomes)
}

func outcomesText(outcomes map[string]string) string {
	pairs := make([]string, 0, len(outcomes))
	for _, key := range slices.Sorted(maps.Keys(outcomes)) {
		pairs = append(pairs, key+"="+outcomes[key])
	}
	return strings.Join(pairs, ", ")
}

// matchRun returns the edge a run close takes and the matched edge's name, so a
// caller can tell a pass arm from any other without re-deriving the match.
// Other than done matches only status=<s>; done tries value edges in sorted key
// order, then count arms, then done, status=done and else.
func matchRun(on map[string]Target, r Result) (string, Target, bool) {
	if r.Status != "done" {
		key := "status=" + r.Status
		t, ok := on[key]
		return edgeName(key, ok), t, ok
	}
	if key, t, ok := matchValue(on, r.Outcomes); ok {
		return key, t, true
	}
	if key, t, ok := matchCount(on, r.Outcomes); ok {
		return key, t, true
	}
	for _, key := range []string{"done", "status=done", "else"} {
		if t, ok := on[key]; ok {
			return key, t, true
		}
	}
	return "", Target{}, false
}

func edgeName(key string, ok bool) string {
	if !ok {
		return ""
	}
	return key
}

// matchValue returns the first sorted name=value edge the close carries; an =0
// edge is a count arm, left to matchCount.
func matchValue(on map[string]Target, outcomes map[string]string) (string, Target, bool) {
	for _, key := range slices.Sorted(maps.Keys(on)) {
		name, value, ok := cutEq(key)
		if !ok || name == "status" || value == "0" {
			continue
		}
		if outcomes[name] == value {
			return key, on[key], true
		}
	}
	return "", Target{}, false
}

func matchCount(on map[string]Target, outcomes map[string]string) (string, Target, bool) {
	for _, key := range slices.Sorted(maps.Keys(on)) {
		if name, ok := strings.CutSuffix(key, ">0"); ok {
			if n, err := strconv.Atoi(outcomes[name]); err == nil && n > 0 {
				return key, on[key], true
			}
			continue
		}
		if name, value, ok := cutEq(key); ok && value == "0" && outcomes[name] == "0" {
			return key, on[key], true
		}
	}
	return "", Target{}, false
}

func cutEq(key string) (string, string, bool) {
	i := strings.IndexByte(key, '=')
	if i < 0 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

func (s State) checkClosed(def Definition, e Event) (State, []Action) {
	s.recordCheck(e)
	t, ok := matchFork(def.Steps[e.Step].On, e.Result)
	if !ok {
		return halt(s, e.Step, e.Step+" "+e.Result+": no edge matches")
	}
	return route(def, s, e.Step, t, 0)
}

// recordStep stores a run step's latest close with its override, so the override
// survives in the state a resume reads back.
func (s State) recordStep(e Event) State {
	s.Results = ensureResults(s.Results)
	s.Results[e.Step] = Result{
		Round: e.Round, Status: e.Status, Outcomes: e.Outcomes,
		Artifacts: e.Artifacts, Override: e.Override,
	}
	return s
}

func (s State) recordCheck(e Event) State {
	s.Results = ensureResults(s.Results)
	s.Results[e.Step] = Result{
		Round:     e.Run,
		Status:    e.Result,
		Outcomes:  map[string]string{"result": e.Result},
		Artifacts: map[string][]string{"log": {e.Log}},
	}
	return s
}

func ensureResults(m map[string]Result) map[string]Result {
	if m == nil {
		return map[string]Result{}
	}
	return m
}

func halt(s State, at, reason string) (State, []Action) {
	s.Status = StatusHalted
	s.Reason = reason
	s.At = at
	return s, []Action{{Kind: ActionHalt, Step: at, Reason: reason}}
}

func stop(s State) (State, []Action) {
	s.Status = StatusStopped
	return s, []Action{{Kind: ActionStop, Step: s.At}}
}

func finish(s State) (State, []Action) {
	s.Status = StatusDone
	return s, []Action{{Kind: ActionFinish, Step: s.At}}
}

func route(def Definition, s State, from string, t Target, walked int) (State, []Action) {
	s.At = from
	switch t.Kind {
	case TargetDone:
		return finish(s)
	case TargetHalt:
		return halt(s, from, RenderParams(def, t.Reason))
	default:
		return enter(def, s, t.Step, walked)
	}
}
