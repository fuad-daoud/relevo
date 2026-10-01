package workflow

import (
	"encoding/json"
	"fmt"
)

// Status is where a run is. Halted, stopped and done are terminal: an event
// that arrives afterwards changes nothing.
type Status string

// The statuses a run holds.
const (
	StatusRunning Status = "running"
	StatusHalted  Status = "halted"
	StatusStopped Status = "stopped"
	StatusDone    Status = "done"
)

// terminal reports whether a status ends the run.
func (st Status) terminal() bool {
	return st == StatusHalted || st == StatusStopped || st == StatusDone
}

// StartInputs are the lists and task a chain is started with. Inputs is the
// workflow's declaration of which of the two it takes; this is the value a
// caller supplies for a run.
type StartInputs struct {
	Plans []string
	Task  string
}

// Awaiting names the close a run waits for: a run step's member and round, a
// check step's run id, or a fork's children.
type Awaiting struct {
	Step     string
	Member   string
	Round    int
	Run      int
	Children []string
}

// Iter is a for-each step's walk: the item index and the items it walks. The
// index is -1 before the first item and after the last. Done tells an exhausted
// walk from one that has not started: both hold -1, and only a projection that
// needs the last item can tell them apart.
type Iter struct {
	Index int
	Items []string
	Done  bool
}

// Result is a step's latest close: its round or run id, its status, the
// outcomes it declared and the artifacts it produced.
type Result struct {
	Round     int
	Status    string
	Outcomes  map[string]string
	Artifacts map[string][]string
}

// State is everything the engine reads and writes.
type State struct {
	Status   Status
	Reason   string
	At       string
	Awaiting Awaiting
	Visits   map[string]int
	Iter     map[string]Iter
	Results  map[string]Result
}

// EventKind names the close an event carries.
type EventKind string

// The closes the engine reads.
const (
	EventStepClosed  EventKind = "step_closed"
	EventCheckClosed EventKind = "check_closed"
	EventChildEnded  EventKind = "child_ended"
	EventNeedsYou    EventKind = "needs_you"
	EventStopped     EventKind = "stopped"
)

// Event is one close. Step and the identity field that fits the event's kind
// select the awaited close; the remaining fields carry what that close
// produced.
type Event struct {
	Kind      EventKind
	Step      string
	Member    string
	Round     int
	Run       int
	Status    string
	Outcomes  map[string]string
	Artifacts map[string][]string
	Result    string
	Log       string
	Child     string
	Reason    string
}

// ActionKind is what relevo does with a transition.
type ActionKind string

// The actions a transition may produce.
const (
	ActionSend     ActionKind = "send"
	ActionRunCheck ActionKind = "run_check"
	ActionFork     ActionKind = "fork"
	ActionMerge    ActionKind = "merge"
	ActionFinish   ActionKind = "finish"
	ActionHalt     ActionKind = "halt"
	ActionStop     ActionKind = "stop"
)

// Action is what relevo does next. A send names the actor and the raw seed; a
// run_check names the command; a halt carries the reason.
type Action struct {
	Kind    ActionKind
	Step    string
	Actor   string
	Seed    string
	Command string
	Reason  string
}

// EncodeEvent writes an event as the one document DecodeEvent reads back. A
// marshal failure yields the empty string, which DecodeEvent rejects rather
// than guessing.
func EncodeEvent(e Event) string {
	b, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeEvent reads a stored event. An unknown kind or a malformed document is
// an error; the stored value is never guessed.
func DecodeEvent(s string) (Event, error) {
	var e Event
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return Event{}, fmt.Errorf("workflow: decode event: %w", err)
	}
	if !e.Kind.known() {
		return Event{}, fmt.Errorf("workflow: unknown event kind %q", string(e.Kind))
	}
	return e, nil
}

// EncodeAction is Action's counterpart of EncodeEvent.
func EncodeAction(a Action) string {
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeAction reads a stored action. An unknown kind or a malformed document
// is an error.
func DecodeAction(s string) (Action, error) {
	var a Action
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return Action{}, fmt.Errorf("workflow: decode action: %w", err)
	}
	if !a.Kind.known() {
		return Action{}, fmt.Errorf("workflow: unknown action kind %q", string(a.Kind))
	}
	return a, nil
}

// known reports whether an event kind is one the engine reads.
func (k EventKind) known() bool {
	switch k {
	case EventStepClosed, EventCheckClosed, EventChildEnded, EventNeedsYou, EventStopped:
		return true
	}
	return false
}

// known reports whether an action kind is one the engine produces.
func (k ActionKind) known() bool {
	switch k {
	case ActionSend, ActionRunCheck, ActionFork, ActionMerge, ActionFinish, ActionHalt, ActionStop:
		return true
	}
	return false
}
