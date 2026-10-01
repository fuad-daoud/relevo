// Package chain owns a chain's state and the pure transition function that
// advances it from one member close to the next. It does no I/O: the state and
// the event alone decide every action.
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

// State is everything the transition function reads and writes.
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
	Findings      int  // security
	FindingsGiven bool // security
	Reason        string
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

// Next advances the chain by one member close. An event that does not name the
// awaited (member, round), or that arrives once the chain has stopped, leaves
// the state untouched, so a replayed close can never advance a chain twice.
func Next(s State, e Event) (State, Action) {
	if s.Status.terminal() {
		return s, Action{}
	}
	if e.Member != s.Awaiting.Member || e.Round != s.Awaiting.Round {
		return s, Action{}
	}

	switch e.Kind {
	case EventBuilderClosed:
		return builderClosed(s, e)
	case EventReviewerClosed:
		return reviewerClosed(s, e)
	case EventPlannerClosed:
		return plannerClosed(s, e)
	case EventSecurityClosed:
		return securityClosed(s, e)
	case EventNeedsYou:
		return halt(s, e.Reason)
	case EventStopped:
		return stop(s)
	default:
		return s, Action{}
	}
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

// builderClosed turns a builder's report into the next step. A halted, blocked
// or unstructured report halts the chain; only a done round, green or red after
// the regate budget, reaches the reviewer.
func builderClosed(s State, e Event) (State, Action) {
	if e.Outcome != reporttail.OutcomeDone {
		return halt(s, fmt.Sprintf("builder halted on plan %d: %s", s.Plan, e.Reason))
	}
	s.Step = StepReviewing
	return send(s, MemberReviewer, SeedReviewer)
}

// reviewerClosed routes on the verdict. A pass advances the plan, starts the
// security phase or finishes; changes seeds a correction while the budget
// lasts; anything else halts.
func reviewerClosed(s State, e Event) (State, Action) {
	switch e.Verdict {
	case VerdictPass:
		return reviewerPassed(s)
	case VerdictChanges:
		if s.Corrections < s.Settings.MaxCorrections {
			s.Step = StepCorrecting
			return send(s, MemberPlanner, SeedCorrection)
		}
		return halt(s, fmt.Sprintf("reviewer still wants changes after %d corrections", s.Settings.MaxCorrections))
	default:
		return halt(s, "reviewer gave no verdict")
	}
}

// reviewerPassed handles a passed plan: the next plan, the security phase, or
// the end of the chain. A pass in the security phase always finishes, because
// the phase scans once and has no further build plan to advance to.
func reviewerPassed(s State) (State, Action) {
	if s.Phase != PhaseSecurity && s.Plan < s.Plans {
		s.Plan++
		s.Corrections = 0
		s.Step = StepBuilding
		return send(s, MemberBuilder, "")
	}
	if s.Phase == PhaseBuild && s.Settings.Security {
		s.Step = StepScanning
		s.Phase = PhaseSecurity
		return send(s, MemberSecurity, SeedSecurity)
	}
	return finish(s)
}

// plannerClosed sends a present correction or fix plan to the builder. A
// correction spends one correction round; a fix plan resets the count because
// the security phase's review loop is its own. A missing plan halts.
func plannerClosed(s State, e Event) (State, Action) {
	if !e.PlanPresent {
		return halt(s, "planner wrote no plan")
	}
	switch s.Step {
	case StepCorrecting:
		s.Corrections++
	case StepPlanningFixes:
		s.Corrections = 0
	default:
		return s, Action{}
	}
	s.Step = StepBuilding
	return send(s, MemberBuilder, "")
}

// securityClosed finishes on no findings and seeds a fix plan otherwise. A
// close without a count halts: the chain never guesses that a scan was clean.
func securityClosed(s State, e Event) (State, Action) {
	if !e.FindingsGiven {
		return halt(s, "security gave no finding count")
	}
	if e.Findings > 0 {
		s.Step = StepPlanningFixes
		return send(s, MemberPlanner, SeedFixes)
	}
	return finish(s)
}

// send names the member the caller must send to and the seed it must render;
// the caller fills Awaiting.Round from that member binding. The round is
// cleared here, so a caller that forgets to fill it can never match a stale
// round the chain happened to hold before the event.
func send(s State, member string, seed SeedKind) (State, Action) {
	s.Awaiting.Member = member
	s.Awaiting.Round = 0
	return s, Action{Kind: ActionSend, Member: member, Seed: seed}
}

func halt(s State, reason string) (State, Action) {
	s.Status = StatusHalted
	s.Reason = reason
	return s, Action{Kind: ActionHalt, Reason: reason}
}

func stop(s State) (State, Action) {
	s.Status = StatusStopped
	return s, Action{Kind: ActionStop}
}

func finish(s State) (State, Action) {
	s.Status = StatusDone
	s.Phase = PhaseFinished
	return s, Action{Kind: ActionFinish}
}

func (st Status) terminal() bool {
	return st == StatusHalted || st == StatusStopped || st == StatusDone
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
