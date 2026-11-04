package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
)

// ResumeAwaitingText is the awaiting line a resume prints for a human. A
// check-await parks with no member round, so the columns read a builder
// round 0 that never ran: the line names the check step from the engine
// state instead, with the reviewed manual round when the resume reviewed
// one. The check's own run number stays off the line: the line reports the
// round the resume reviewed, not the run the engine opened. Any other
// state, and any row without state, keeps the columns line.
func ResumeAwaitingText(c db.ChainRow, manualRound int) string {
	columns := fmt.Sprintf("awaiting %s round %d", c.AwaitingMember, c.AwaitingRound)
	def, derr := chain.WorkflowDef(c)
	st, serr := chain.WorkflowState(c)
	if derr != nil || serr != nil {
		return columns
	}
	step := st.Awaiting.Step
	if st.Awaiting.Member != "" || step == "" || def.Steps[step].Check == "" {
		return columns
	}
	if manualRound > 0 {
		return fmt.Sprintf("awaiting check %s on manual round %d", step, manualRound)
	}
	return fmt.Sprintf("awaiting check %s", step)
}
