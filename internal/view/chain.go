package view

import (
	"fmt"
	"strings"
)

// ChainFacts is one chain's state as a status surface shows it: the state
// machine's own words, the plan the chain holds, and the member it waits on.
// They are plain strings and counts, so a chain row renders without view
// importing the state machine that owns them.
type ChainFacts struct {
	Status      string `json:"status"` // running | halted | stopped | done
	Phase       string `json:"phase"`
	Step        string `json:"step"`
	Plan        int    `json:"plan"`
	Plans       int    `json:"plans"`
	Corrections int    `json:"corrections"`
	Awaiting    string `json:"awaiting,omitempty"` // the active member
	Reason      string `json:"reason,omitempty"`
	// ManualRound is the builder's current round while a halted or stopped
	// chain has a manual round running on it; 0 when nothing is in flight.
	// It is the fact that turns the row from NEEDS YOU into the chain's own
	// status word with the round in the segment.
	ManualRound int `json:"manual_round,omitempty"`
	// StepAt, Round, PlanPos, PlanTotal and Check describe a workflow chain:
	// the step its engine is on, the round or check run it awaits, and its
	// position in the plan input. They are empty on a chain that runs the
	// fixed state machine.
	StepAt    string `json:"step_at,omitempty"`
	Round     int    `json:"round,omitempty"`
	PlanPos   int    `json:"plan_pos,omitempty"`
	PlanTotal int    `json:"plan_total,omitempty"`
	Check     bool   `json:"check,omitempty"`
	// Parent is the chain a fork's child belongs to, empty on every other
	// chain. A row that carries it is not a top-level row: it prints indented
	// under the parent it names.
	Parent string `json:"parent,omitempty"`
	// Children are the parent chain's child chain names in key order, and
	// ChildrenDone how many of them have ended. Both are empty on a chain with
	// no fork, so its row renders exactly as it did before forks existed.
	Children     []string `json:"children,omitempty"`
	ChildrenDone int      `json:"children_done,omitempty"`
}

// ChainForkSegment is the fork progress a parent chain's row shows after its
// own step: "fork split · 1/2 done". A chain with no children returns "", so a
// row without a fork keeps the text it had.
func ChainForkSegment(f ChainFacts) string {
	if len(f.Children) == 0 {
		return ""
	}
	return fmt.Sprintf("fork split · %d/%d done", f.ChildrenDone, len(f.Children))
}

// ChainSegment is the text a chain row shows after its name: the plan in
// progress, the step the active member is on and the correction rounds spent,
// for example "plan 2/4 · reviewing · 1 correction". Zero corrections leaves
// the segment off entirely; one takes the singular.
//
// A halted or stopped chain with a manual round running reads only that round
// ("manual round 3 running"). A done or stopped chain omits the step: the work
// is over, and the step word no longer describes anything happening; a halted
// chain keeps it.
func ChainSegment(f ChainFacts) string {
	if f.ManualRound > 0 {
		return fmt.Sprintf("manual round %d running", f.ManualRound)
	}
	if f.StepAt != "" {
		return flowChainSegment(f)
	}
	s := fmt.Sprintf("plan %d/%d", f.Plan, f.Plans)
	if f.Step != "" && f.Status != "done" && f.Status != "stopped" {
		s += " · " + f.Step
	}
	if f.Corrections > 0 {
		s += fmt.Sprintf(" · %d correction", f.Corrections)
		if f.Corrections != 1 {
			s += "s"
		}
	}
	return s
}

// flowChainSegment is the text a workflow chain's row shows: the step its
// engine is on with the round it awaits -- or the check run while a check is
// awaited -- and its position in the plan input.
func flowChainSegment(f ChainFacts) string {
	s := ""
	if f.Status != "done" && f.Status != "stopped" {
		if f.Check {
			s = fmt.Sprintf("%s check run %d", f.StepAt, f.Round)
		} else {
			s = fmt.Sprintf("%s r%d", f.StepAt, f.Round)
		}
	}
	if f.PlanTotal > 0 {
		if s != "" {
			s += " · "
		}
		s += fmt.Sprintf("plans %d/%d", f.PlanPos, f.PlanTotal)
	}
	if fork := ChainForkSegment(f); fork != "" {
		if s != "" {
			s += " · "
		}
		s += fork
	}
	return s
}

// ChainDisplay maps a chain's status to the word a status surface shows: a
// halted or stopped chain waits on a human, a done one is finished, and a
// running one is working. It is the chain rows' counterpart of DisplayState.
//
// A halted or stopped chain whose builder has a manual round running is not
// waiting on a human: it reads as its own status word, so the row does not
// claim NEEDS YOU while work is in flight.
func ChainDisplay(f ChainFacts) string {
	switch f.Status {
	case "halted", "stopped":
		if f.ManualRound > 0 {
			return strings.ToUpper(f.Status)
		}
		return "NEEDS YOU"
	case "done":
		return "DONE"
	default:
		return "ACTIVE"
	}
}
