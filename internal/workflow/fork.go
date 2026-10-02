package workflow

import (
	"fmt"
	"strconv"
)

// enterFork enters a fork step: it starts fixed children or an each list, or
// routes joined when an each list is empty.
func enterFork(def Definition, s State, id string, step Step, walked int) (State, []Action) {
	if step.Fork == nil {
		return halt(s, id, id+": not a step kind")
	}
	var specs []ChildSpec
	if step.Fork.Children != nil {
		specs = make([]ChildSpec, len(step.Fork.Children))
		for i, c := range step.Fork.Children {
			specs[i] = ChildSpec{
				Key:      strconv.Itoa(i + 1),
				Workflow: c.Workflow,
				Task:     c.Task,
				Plans:    append([]string(nil), c.Plans...),
			}
		}
	} else {
		items := forkItems(s, id, step.Fork.Each)
		if len(items) == 0 {
			s.Results = ensureResults(s.Results)
			s.Results[id] = Result{
				Status:   "joined",
				Outcomes: map[string]string{"result": "joined"},
			}
			s.Awaiting = Awaiting{}
			walked++
			if walked > len(def.Steps)+1 {
				return halt(s, id, capReason(def, id))
			}
			t, ok := matchFork(step.On, "joined")
			if !ok {
				return halt(s, id, id+" joined: no edge matches")
			}
			return route(def, s, id, t, walked)
		}
		specs = make([]ChildSpec, len(items))
		for i, item := range items {
			specs[i] = ChildSpec{
				Key:      strconv.Itoa(i + 1),
				Workflow: step.Fork.Workflow,
				Plans:    []string{item},
			}
		}
	}
	keys := make([]string, len(specs))
	for i, sp := range specs {
		keys[i] = sp.Key
	}
	s.Awaiting = Awaiting{
		Step:     id,
		Children: keys,
	}
	return s, []Action{{
		Kind:     ActionFork,
		Step:     id,
		Children: specs,
	}}
}

// forkItems reads the items an each fork walks, from the seeded plans input or
// an artifact in Results.
func forkItems(s State, id, source string) []string {
	if source == "plans" {
		return s.Iter[id].Items
	}
	root, attr, ok := artifactSource(source)
	if !ok {
		return nil
	}
	if r, ok := s.Results[root]; ok {
		return r.Artifacts[attr]
	}
	return nil
}

// childEnded records one child's end, and once all children have ended, routes
// to halted or emits a merge action.
func (s State) childEnded(def Definition, e Event) (State, []Action) {
	ended := make(map[string]ChildEnd, len(s.Awaiting.Ended)+1)
	for k, v := range s.Awaiting.Ended {
		ended[k] = v
	}
	ended[e.Child] = ChildEnd{Status: e.Status, Reason: e.Reason}
	s.Awaiting.Ended = ended

	for _, k := range s.Awaiting.Children {
		if _, ok := s.Awaiting.Ended[k]; !ok {
			return s, nil
		}
	}

	var firstHaltedKey string
	var firstHaltedEnd ChildEnd
	anyNotDone := false
	for _, k := range s.Awaiting.Children {
		end := s.Awaiting.Ended[k]
		if end.Status != "done" {
			if !anyNotDone {
				anyNotDone = true
				firstHaltedKey = k
				firstHaltedEnd = end
			}
		}
	}

	if anyNotDone {
		s.Results = ensureResults(s.Results)
		s.Results[e.Step] = Result{
			Status:   "halted",
			Outcomes: map[string]string{"result": "halted"},
		}
		step := def.Steps[e.Step]
		if t, ok := step.On["result=halted"]; ok {
			return route(def, s, e.Step, t, 0)
		}
		if t, ok := step.On["halted"]; ok {
			return route(def, s, e.Step, t, 0)
		}
		reason := firstHaltedEnd.Reason
		if firstHaltedEnd.Status == "stopped" {
			reason = fmt.Sprintf("child %s stopped", firstHaltedKey)
		}
		return halt(s, e.Step, reason)
	}

	s.Awaiting.Merging = true
	return s, []Action{{Kind: ActionMerge, Step: e.Step}}
}

// mergeClosed records a fork's merge result and routes on the result.
func (s State) mergeClosed(def Definition, e Event) (State, []Action) {
	s.Results = ensureResults(s.Results)
	s.Results[e.Step] = Result{
		Status:    e.Result,
		Outcomes:  map[string]string{"result": e.Result},
		Artifacts: e.Artifacts,
	}
	s.Awaiting = Awaiting{}
	t, ok := matchFork(def.Steps[e.Step].On, e.Result)
	if !ok {
		return halt(s, e.Step, e.Step+" "+e.Result+": no edge matches")
	}
	return route(def, s, e.Step, t, 0)
}

// matchFork returns the edge a fork outcome takes: result=<v>, then the bare
// <v>, then else.
func matchFork(on map[string]Target, result string) (Target, bool) {
	if t, ok := on["result="+result]; ok {
		return t, true
	}
	if t, ok := on[result]; ok {
		return t, true
	}
	t, ok := on["else"]
	return t, ok
}

// ReopenFork re-opens an awaited fork at a halted child when the parent is
// halted at the fork step.
func ReopenFork(s State, child string) (State, bool) {
	if s.Status != StatusHalted {
		return s, false
	}
	if s.Awaiting.Step == "" || s.At != s.Awaiting.Step {
		return s, false
	}
	if len(s.Awaiting.Children) == 0 {
		return s, false
	}
	end, ok := s.Awaiting.Ended[child]
	if !ok || end.Status == "done" {
		return s, false
	}
	ended := make(map[string]ChildEnd, len(s.Awaiting.Ended))
	for k, v := range s.Awaiting.Ended {
		if k != child {
			ended[k] = v
		}
	}
	s.Status = StatusRunning
	s.Reason = ""
	s.Awaiting.Ended = ended
	s.Awaiting.Merging = false
	return s, true
}
