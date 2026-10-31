package chain

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// WorkflowDef decodes a chain's stored workflow definition. The stored
// form is the workflow package's own JSON, so it is read back through its
// parser, which owns the step and target shapes.
func WorkflowDef(c db.ChainRow) (workflow.Definition, error) {
	if len(c.WorkflowJSON) == 0 {
		return workflow.Definition{}, fmt.Errorf("chain %s carries no workflow", c.Name)
	}
	def, err := workflow.Parse(c.WorkflowJSON)
	if err != nil {
		return workflow.Definition{}, fmt.Errorf("chain %s workflow: %w", c.Name, err)
	}
	return def, nil
}

// WorkflowState decodes a chain's stored engine state.
func WorkflowState(c db.ChainRow) (workflow.State, error) {
	var st workflow.State
	if len(c.StateJSON) == 0 {
		return st, fmt.Errorf("chain %s has no engine state", c.Name)
	}
	if err := json.Unmarshal(c.StateJSON, &st); err != nil {
		return workflow.State{}, fmt.Errorf("chain %s state: %w", c.Name, err)
	}
	return st, nil
}

// FlowActorMatch reports whether a stored actor names the actor a
// workflow's run step asked for. A builder binding stores no role word, so an
// empty stored actor matches the builder.
func FlowActorMatch(stored, actor string) bool {
	return FlowActorName(stored) == actor
}

// FlowActorName is the actor a stored chain-member role names.
func FlowActorName(stored string) string {
	if stored == "" {
		return "builder"
	}
	return stored
}
