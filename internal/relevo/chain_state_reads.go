package relevo

import (
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainStoredFacts is a chain's fixed-column facts as a read surface shows
// them: the engine state's legacy projection when the row carries a state, the
// stored columns otherwise.
type chainStoredFacts struct {
	Phase       string
	Step        string
	Plan        int
	Plans       int
	Corrections int
	Awaiting    string
}

// chainStoredFactsOf projects a chain's facts onto the legacy columns a read
// surface shows. A row with a state reads them from the state through
// workflow.LegacyView, so the columns themselves stay unread; the plan count is
// kept at least one, as the row's own validity asks.
func chainStoredFactsOf(c db.ChainRow) chainStoredFacts {
	if len(c.StateJSON) > 0 {
		def, derr := chain.WorkflowDef(c)
		st, serr := chain.WorkflowState(c)
		if derr == nil && serr == nil {
			lf := workflow.LegacyView(def, st)
			plan, plans := lf.Plan, lf.Plans
			if plans < 1 {
				plan, plans = 1, 1
			}
			return chainStoredFacts{
				Phase: lf.Phase, Step: lf.Step, Plan: plan, Plans: plans,
				Corrections: lf.Corrections, Awaiting: lf.AwaitingMember,
			}
		}
	}
	return chainStoredFacts{
		Phase: c.Phase, Step: c.Step, Plan: c.Plan, Plans: c.Plans,
		Corrections: c.Corrections, Awaiting: c.AwaitingMember,
	}
}
