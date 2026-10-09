package view

// ChainProgress is a chain row's position as numbers, from the same ChainFacts
// the row's Chain text renders from.
type ChainProgress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Phase string `json:"phase"`
}

// chainProgressOf maps a fixed chain's Plan/Plans/Step and a flow chain's
// PlanPos/PlanTotal/StepAt, the pairs ChainSegment prints.
func chainProgressOf(f ChainFacts) *ChainProgress {
	if f.StepAt != "" {
		return &ChainProgress{Done: f.PlanPos, Total: f.PlanTotal, Phase: f.StepAt}
	}
	return &ChainProgress{Done: f.Plan, Total: f.Plans, Phase: f.Step}
}
