package view

import "fmt"

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
}

// ChainSegment is the text a chain row shows after its name: the plan in
// progress, the step the active member is on and the correction rounds spent,
// for example "plan 2/4 · reviewing · 1 correction". Zero corrections leaves
// the segment off entirely; one takes the singular.
func ChainSegment(f ChainFacts) string {
	s := fmt.Sprintf("plan %d/%d", f.Plan, f.Plans)
	if f.Step != "" {
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

// ChainDisplay maps a chain's status to the word a status surface shows: a
// halted or stopped chain waits on a human, a done one is finished, and a
// running one is working. It is the chain rows' counterpart of DisplayState.
func ChainDisplay(f ChainFacts) string {
	switch f.Status {
	case "halted", "stopped":
		return "NEEDS YOU"
	case "done":
		return "DONE"
	default:
		return "ACTIVE"
	}
}
