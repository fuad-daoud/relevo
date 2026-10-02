// Package chain reads trace rows written before workflows carried them: it
// decodes their event and action documents and renders them as trace lines. It
// also holds the member, phase, step and gate words those rows and a chain's
// persisted columns share.
package chain

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

// Status is where a chain is. Halted, stopped and done are terminal: a close
// that arrives afterwards changes nothing.
type Status string

// The statuses a chain holds.
const (
	StatusRunning Status = "running"
	StatusHalted  Status = "halted"
	StatusStopped Status = "stopped"
	StatusDone    Status = "done"
)

// Phase is the part of the run a chain is in. The security phase scans once:
// after its fix plan passes review the chain finishes, so a pass in that phase
// never advances to another plan.
type Phase string

// The phases a chain holds.
const (
	PhaseBuild    Phase = "build"
	PhaseSecurity Phase = "security"
	PhaseFinished Phase = "finished"
)

// Step is what the active member is doing.
type Step string

// The steps a chain holds.
const (
	StepBuilding      Step = "building"
	StepReviewing     Step = "reviewing"
	StepCorrecting    Step = "correcting"
	StepScanning      Step = "scanning"
	StepPlanningFixes Step = "planning-fixes"
)

// Verdict is a reviewer's judgement. Its zero value means the reviewer gave
// none.
type Verdict string

// The verdicts a reviewer may give.
const (
	VerdictPass    Verdict = "pass"
	VerdictChanges Verdict = "changes"
)

// Member part names. A member is named by the part it fills, never by its
// binding name: the chain row owns the binding names.
const (
	MemberBuilder  = "builder"
	MemberReviewer = "reviewer"
	MemberPlanner  = "planner"
	MemberSecurity = "security"
)

// Gate results a builder close can carry. None is a round with no check at
// all; a red gate has already spent the regate budget, so it still reaches the
// reviewer rather than a halt.
const (
	GateGreen = "green"
	GateRed   = "red"
	GateNone  = "none"
)

// Settings is the chain's resolved configuration, stored when the chain starts
// so a later settings edit cannot change a running chain.
type Settings struct {
	MaxCorrections int
	ReviewerActor  string
	PlannerActor   string
	SecurityActor  string
	Security       bool
	// Gate is the builder member's resolved acceptance command; "" means no
	// check. Regate is its repair-round budget.
	Gate   string
	Regate int
}

// Awaiting names the member round whose close the chain waits for. The caller
// fills the round when it sends to the member binding the action names, so a
// stored round is never 0.
type Awaiting struct {
	Member string
	Round  int
}

// State is everything the transition function read and wrote.
type State struct {
	Status      Status
	Reason      string
	Phase       Phase
	Step        Step
	Plan        int
	Plans       int
	Corrections int
	Awaiting    Awaiting
	Settings    Settings
}

// EventKind names the close that produced an event.
type EventKind string

// The closes that reach a chain.
const (
	EventBuilderClosed  EventKind = "builder_closed"
	EventReviewerClosed EventKind = "reviewer_closed"
	EventPlannerClosed  EventKind = "planner_closed"
	EventSecurityClosed EventKind = "security_closed"
	EventNeedsYou       EventKind = "needs_you"
	EventStopped        EventKind = "stopped"
)

// Event is one member close. Member and Round identify the round that closed;
// the remaining fields carry only what that kind of close produces.
type Event struct {
	Kind          EventKind
	Member        string
	Round         int
	Outcome       string // builder: the report tail's outcome
	Gate          string // builder: green, red after the regate budget, or none when no check ran
	Verdict       Verdict
	PlanPresent   bool // planner: artifact present and non-empty
	Reason        string
	Findings      int  // security
	FindingsGiven bool // security
}

// ActionKind is what relevo does with a transition. Its zero value is no
// action at all.
type ActionKind string

// The actions a transition may produce.
const (
	ActionNone   ActionKind = ""
	ActionSend   ActionKind = "send"
	ActionHalt   ActionKind = "halt"
	ActionStop   ActionKind = "stop"
	ActionFinish ActionKind = "finish"
)

// SeedKind names a seed template. A send to the builder carries no seed: the
// caller sends the plan file itself.
type SeedKind string

// The seeds a chain renders.
const (
	SeedReviewer   SeedKind = "reviewer"
	SeedCorrection SeedKind = "correction"
	SeedSecurity   SeedKind = "security"
	SeedFixes      SeedKind = "fixes"
)

// Action is what relevo does next. A send names the member and the seed; the
// caller supplies the round from the binding it sends to. A halt carries the
// reason the chain stopped.
type Action struct {
	Kind   ActionKind
	Member string
	Seed   SeedKind
	Reason string
}

// BuilderHaltReason is the one-line reason a builder close that is not done
// carries onto the chain: the first present of the report tail's halted_at,
// the close note and the first not_done item, each labelled; the outcome word
// when none of them is set. A value that carries a newline is cut at its first
// one and trimmed before it is labelled, and an empty source is skipped rather
// than rendered as an empty label. Pure.
func BuilderHaltReason(tail reporttail.Tail, note, outcome string) string {
	for _, src := range []struct{ label, value string }{
		{"halted_at", tail.HaltedAt},
		{"note", note},
		{"not_done", firstOrEmpty(tail.NotDone)},
	} {
		if v := firstLine(src.value); v != "" {
			return src.label + ": " + v
		}
	}
	return "status: " + outcome
}

// firstOrEmpty is the first element of a list, or "" for a list with none.
func firstOrEmpty(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// firstLine is value cut at its first newline and trimmed, or "" when nothing
// but whitespace is left.
func firstLine(value string) string {
	if idx := strings.IndexByte(value, '\n'); idx != -1 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

// Encode writes the event as the one document DecodeEvent reads back. Marshal
// cannot fail for these scalar fields; it yields the empty string, which
// DecodeEvent rejects rather than guessing.
func (e Event) Encode() string {
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
		return Event{}, fmt.Errorf("chain: decode event: %w", err)
	}
	if !e.Kind.known() {
		return Event{}, fmt.Errorf("chain: unknown event kind %q", string(e.Kind))
	}
	return e, nil
}

// Encode is Action's counterpart of Event.Encode.
func (a Action) Encode() string {
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
		return Action{}, fmt.Errorf("chain: decode action: %w", err)
	}
	if !a.Kind.known() {
		return Action{}, fmt.Errorf("chain: unknown action kind %q", string(a.Kind))
	}
	return a, nil
}

func (k EventKind) known() bool {
	switch k {
	case EventBuilderClosed, EventReviewerClosed, EventPlannerClosed, EventSecurityClosed, EventNeedsYou, EventStopped:
		return true
	}
	return false
}

func (k ActionKind) known() bool {
	switch k {
	case ActionNone, ActionSend, ActionHalt, ActionStop, ActionFinish:
		return true
	}
	return false
}

// TraceLine is one chain_event row as the trace's line renderer reads it: the
// plan the chain was on, the phase and step before the event, the member whose
// round closed and its round, the event and the action it produced, and the
// halt reason when there is one. It carries the state machine's own types, so
// the renderer needs no second copy of them.
type TraceLine struct {
	Plan, Plans int
	Phase       Phase
	Step        Step
	Member      string
	Round       int
	Event       Event
	Action      Action
	Reason      string
}

// Line renders l as one trace line: plan i/N, the state before the event, the
// closing member and its round, then the event's detail. The columns are padded
// so a trace lines up, and a halt appends its reason -- but only when the
// rendered detail does not already carry that same string, so a needs-you row
// whose detail is the event's own reason prints it once, while a distinct
// event detail and action reason both still show.
func (l TraceLine) Line() string {
	detail := l.detail()
	line := fmt.Sprintf("plan %d/%d  %-8s %-9s  %s",
		l.Plan, l.Plans, l.stateWord(), l.memberRound(), detail)
	if l.Reason != "" && !strings.Contains(detail, l.Reason) {
		line += "  " + l.Reason
	}
	return line
}

// stateWord is the step the chain was on before the event, in the short word
// the design's example uses. A step the state machine does not name falls back
// to its own text, and an empty step to the phase, so a row is never wordless.
func (l TraceLine) stateWord() string {
	if w := l.Step.Word(); w != "" {
		return w
	}
	if l.Step != "" {
		return string(l.Step)
	}
	return string(l.Phase)
}

// Word is the step's short word, the vocabulary the trace's state column and a
// resume's reason both speak: build, review, correct, scan, planning. A step
// the state machine does not name has no word.
func (s Step) Word() string {
	switch s {
	case StepBuilding:
		return "build"
	case StepReviewing:
		return "review"
	case StepCorrecting:
		return "correct"
	case StepScanning:
		return "scan"
	case StepPlanningFixes:
		return "planning"
	}
	return ""
}

// ResumeReason is the reason a resumed chain's trace row carries: where the
// resume moved the chain, in the trace's own step words -- "resumed -> build",
// "resumed -> review" and so on. A step with no word yields just the arrow.
func ResumeReason(s Step) string { return "resumed -> " + s.Word() }

// memberRound is the closing member and its round, the line's third column.
func (l TraceLine) memberRound() string {
	return fmt.Sprintf("%s r%d", l.Member, l.Round)
}

// detail is what the member's close said: the builder's gate, the reviewer's
// verdict, the planner's plan, the security finding count, or the event's own
// reason for a needs-you or stopped close.
func (l TraceLine) detail() string {
	switch l.Event.Kind {
	case EventBuilderClosed:
		return gateWord(l.Event.Gate)
	case EventReviewerClosed:
		return verdictWord(l.Event.Verdict)
	case EventPlannerClosed:
		if l.Step == StepPlanningFixes {
			return "fix plan"
		}
		return "correction plan"
	case EventSecurityClosed:
		if !l.Event.FindingsGiven {
			return "no findings given"
		}
		return findingWord(l.Event.Findings)
	case EventNeedsYou:
		if l.Event.Reason != "" {
			return l.Event.Reason
		}
		return "needs you"
	case EventStopped:
		return "stopped"
	}
	return string(l.Event.Kind)
}

// gateWord is a builder close's gate as the trace shows it. A red gate reads
// as a plain red check: the regate budget it spent is the state machine's own
// fact, not the line's. A round with no check says so rather than reading as
// green.
func gateWord(gate string) string {
	switch gate {
	case GateNone:
		return "no check"
	case GateRed:
		return "check red"
	}
	return "check green"
}

// verdictWord is a reviewer close's verdict as the trace shows it, the zero
// verdict included.
func verdictWord(v Verdict) string {
	switch v {
	case VerdictPass:
		return "pass"
	case VerdictChanges:
		return "changes"
	}
	return "no verdict"
}

// findingWord is a security close's finding count in words.
func findingWord(n int) string {
	switch n {
	case 0:
		return "no findings"
	case 1:
		return "1 finding"
	}
	return fmt.Sprintf("%d findings", n)
}
