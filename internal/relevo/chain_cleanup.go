package relevo

import (
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainStartResult refreshes the started row and reports the start line's
// check and cleanup: the commands the check and cleanup steps run, with the
// tier that supplied each and the cleanup's when word.
func chainStartResult(tx *store.Tx, prep chainPreparedWorkflow, row db.ChainRow, stored []store.Binding) ChainResult {
	if current, cerr := tx.Chain(prep.opts.Name); cerr == nil {
		row = current
	}
	check, source := chain.CheckReport(prep.plan.def, prep.plan.gateRes, prep.opts.Params)
	cleanup, cleanupSource, cleanupAfter := chain.CleanupReport(prep.plan.def,
		prep.plan.cleanupRes.Command, prep.plan.cleanupRes.Source, prep.plan.cleanupRes.After, prep.opts.Params)
	return ChainResult{Chain: row, Members: stored, Plans: len(prep.plan.bodies),
		Check: check, CheckSource: source,
		Cleanup: cleanup, CleanupSource: cleanupSource, CleanupAfter: cleanupAfter}
}
