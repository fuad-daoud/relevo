package view

import "strings"

// ChainProgress is a chain row's position as numbers, from the same ChainFacts
// the row's Chain text renders from.
type ChainProgress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Phase string `json:"phase"`
}

// chainProgressOf maps a fixed chain's Plan/Plans/Step and a flow chain's
// PlanPos/PlanTotal/StepAt, the pairs ChainSegment prints. Those positions are
// the plan being worked, 1-based, so Done is one less until the chain is done.
// A finished or stopped chain's step names nothing running, so its status
// stands in for it; a halted chain keeps the step it halted on, as ChainSegment
// does.
func chainProgressOf(f ChainFacts) *ChainProgress {
	pos, total, phase := f.Plan, f.Plans, f.Step
	if f.StepAt != "" {
		pos, total, phase = f.PlanPos, f.PlanTotal, f.StepAt
	}
	done := pos - 1
	switch f.Status {
	case "done":
		done, phase = total, f.Status
	case "stopped":
		phase = f.Status
	case "halted":
		phase = strings.TrimSpace("halted " + phase)
	}
	return &ChainProgress{Done: max(0, done), Total: total, Phase: phase}
}
