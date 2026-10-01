package chain

import (
	"fmt"
	"strings"
)

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
