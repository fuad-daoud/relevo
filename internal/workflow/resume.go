package workflow

import (
	"fmt"
	"strings"
)

// ResumeOpts selects how a resume re-enters a chain: the step to re-enter, or a
// close a human sent after the chain halted.
type ResumeOpts struct {
	From   string
	Closed *Event
}

// Resume re-enters a halted or stopped chain. A running or done chain is
// refused: there is nothing to resume. With no close, it re-enters the named
// step, or the step the chain halted on, resetting that step's budget count so
// it runs again. With a close, it treats a newer manual round for the awaited
// run step as that step's close and routes it.
func Resume(def Definition, s State, opts ResumeOpts) (State, []Action, error) {
	if s.Status == StatusRunning || s.Status == StatusDone {
		return State{}, nil, fmt.Errorf("workflow: cannot resume a %s run", s.Status)
	}
	if opts.Closed != nil {
		return resumeClosed(def, s, opts, *opts.Closed)
	}
	return resumeAt(def, s, opts.From)
}

// resumeAt re-enters a chain at a step, resetting that step's own budget count
// so the entry runs it again. From names the step; empty means the step the
// chain is on.
func resumeAt(def Definition, s State, from string) (State, []Action, error) {
	target := from
	if target == "" {
		target = s.At
	}
	if _, ok := def.Steps[target]; !ok {
		return State{}, nil, unknownStepError(def, target)
	}
	s = ensureMaps(s)
	s.Visits[target] = 0
	s.Reason = ""
	s.Status = StatusRunning
	state, actions := enter(def, s, target, 0)
	return state, actions, nil
}

// resumeClosed applies a close a human sent after the chain halted: a newer
// round for the run step the chain is on. A close that is not a newer round for
// that step's actor is refused rather than routed, so a foreign close can never
// advance the chain.
func resumeClosed(def Definition, s State, opts ResumeOpts, e Event) (State, []Action, error) {
	if opts.From != "" {
		return State{}, nil, fmt.Errorf("workflow: cannot resume from %q when a close is given", opts.From)
	}
	if e.Kind != EventStepClosed {
		return State{}, nil, fmt.Errorf("workflow: resume close is a %s, not a step close", string(e.Kind))
	}
	step, ok := def.Steps[s.At]
	if !ok {
		return State{}, nil, unknownStepError(def, s.At)
	}
	if kindOf(step) != "run" {
		return State{}, nil, fmt.Errorf("workflow: %s is not a run step", s.At)
	}
	actor := RenderParams(def, step.Run)
	switch {
	case e.Step != s.At:
		return State{}, nil, fmt.Errorf("workflow: close names step %q, not %q", e.Step, s.At)
	case e.Member != actor:
		return State{}, nil, fmt.Errorf("workflow: close is from %q, not %q", e.Member, actor)
	case e.Round <= s.Awaiting.Round:
		return State{}, nil, fmt.Errorf("workflow: close round %d is not newer than %d", e.Round, s.Awaiting.Round)
	}
	s = ensureMaps(s)
	s.Status = StatusRunning
	s.Awaiting = Awaiting{Step: s.At, Member: actor, Round: e.Round}
	state, actions := Next(def, s, e)
	return state, actions, nil
}

// unknownStepError names a resume target that is not a step and lists the steps
// the workflow has, so the caller can pick one.
func unknownStepError(def Definition, target string) error {
	return fmt.Errorf("workflow: unknown step %q; the workflow has: %s", target, strings.Join(ResumeTargets(def), ", "))
}

// ResumeTargets returns every step a resume could name, sorted.
func ResumeTargets(def Definition) []string {
	return sortedKeys(def.Steps)
}

// Current returns the item a for-each step is on, with its 1-based position and
// the total item count. ok is false when the step has no walk or is between
// items, in which case pos is zero.
func Current(s State, forEach string) (item string, pos, total int, ok bool) {
	it, found := s.Iter[forEach]
	if !found {
		return "", 0, 0, false
	}
	total = len(it.Items)
	if it.Index < 0 || it.Index >= total {
		return "", 0, total, false
	}
	return it.Items[it.Index], it.Index + 1, total, true
}
