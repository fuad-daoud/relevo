package relevo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumeWorkflow continues a halted or stopped workflow chain: it
// re-enters the step the engine is on -- or the one --from names -- applies
// --param to the stored definition, and runs the resumed action.
func chainResumeWorkflow(ctx context.Context, rt Runtime, c db.ChainRow, opts ResumeOptions) (ChainResult, error) {
	if err := resumeRefusal(c); err != nil {
		return ChainResult{}, err
	}
	def, err := chainWorkflowDef(c)
	if err != nil {
		return ChainResult{}, err
	}
	before, err := chainWorkflowState(c)
	if err != nil {
		return ChainResult{}, err
	}
	if len(opts.Params) > 0 {
		if def, err = workflow.WithParams(def, opts.Params); err != nil {
			return ChainResult{}, refuse("%v", err)
		}
		given := workflow.Given{Plans: len(before.Iter["plans"].Items) > 0}
		if err := chainValidateWorkflow(rt, def, given); err != nil {
			return ChainResult{}, err
		}
	}
	if err := supersedeChainDelivery(rt, c); err != nil {
		return ChainResult{}, err
	}

	var out ChainResult
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain(opts.Name)
		if err != nil {
			return err
		}
		if err := resumeRefusal(row); err != nil {
			return err
		}
		resumed, acts, err := workflow.Resume(def, before, workflow.ResumeOpts{From: opts.From})
		if err != nil {
			return refuse("%v", err)
		}
		defJSON, err := json.Marshal(def)
		if err != nil {
			return fmt.Errorf("encode chain workflow: %w", err)
		}
		row.WorkflowJSON = defJSON
		row.Status = string(resumed.Status)
		stateJSON, err := json.Marshal(resumed)
		if err != nil {
			return fmt.Errorf("encode chain state: %w", err)
		}
		row.StateJSON = stateJSON
		applyChainLegacy(&row, def, resumed)
		row.UpdatedAt = rt.Now().UTC()
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		for _, act := range acts {
			if err := chainRunAction(ctx, rt, tx, row, def, resumed, &resumed, workflow.Event{Kind: workflow.EventNeedsYou, Step: resumed.At}, act); err != nil {
				return err
			}
		}
		members, err := chainFlowStoredMembers(tx, row)
		if err != nil {
			return err
		}
		out = ChainResult{Chain: row, Members: members, Plans: len(before.Iter["plans"].Items)}
		return nil
	})
	if err != nil {
		return ChainResult{}, err
	}
	return out, nil
}

// chainFlowStoredMembers loads a workflow chain's member bindings in
// chain_member order.
func chainFlowStoredMembers(tx *store.Tx, c db.ChainRow) ([]store.Binding, error) {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return nil, err
	}
	out := make([]store.Binding, 0, len(rows))
	for _, m := range rows {
		b, lerr := tx.Load(m.Binding)
		if lerr != nil {
			return nil, lerr
		}
		out = append(out, b)
	}
	return out, nil
}
