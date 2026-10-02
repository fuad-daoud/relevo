package roles

import "github.com/fuad-daoud/relevo/internal/workflow"

// shippedAgentOutputs defines the default outputs for shipped agents.
var shippedAgentOutputs = map[string]workflow.Outputs{
	"reviewer": {
		"verdict":  {Kind: workflow.OutputOneOf, Values: []string{"pass", "changes"}},
		"findings": {Kind: workflow.OutputArtifact},
	},
	"security-reviewer": {
		"findings": {Kind: workflow.OutputCount},
		"report":   {Kind: workflow.OutputArtifact},
	},
	"architect": {
		"plan": {Kind: workflow.OutputArtifact},
	},
	"plan-executor": {},
	"researcher":    {},
}

// EffectiveOutputs returns an actor's effective outputs: its declared outputs
// when present, or its shipped agent's default declaration. An undeclared actor
// on an unknown or non-shipped agent has no outputs.
func EffectiveOutputs(agent string, declared workflow.Outputs) workflow.Outputs {
	if len(declared) > 0 {
		return copyOutputs(declared)
	}
	defaults, ok := shippedAgentOutputs[agent]
	if !ok {
		return nil
	}
	return copyOutputs(defaults)
}

// copyOutputs returns a deep copy of outputs.
func copyOutputs(o workflow.Outputs) workflow.Outputs {
	if o == nil {
		return nil
	}
	out := make(workflow.Outputs, len(o))
	for k, v := range o {
		var values []string
		if v.Values != nil {
			values = append([]string(nil), v.Values...)
		}
		out[k] = workflow.Output{
			Kind:   v.Kind,
			Values: values,
		}
	}
	return out
}
