package workflow

import "slices"

// ShippedOutputs returns the default outputs for a shipped agent, returning a
// fresh map each call, and whether the agent is known.
func ShippedOutputs(agent string) (Outputs, bool) {
	switch agent {
	case "reviewer":
		return Outputs{
			"verdict":  {Kind: OutputOneOf, Values: []string{"pass", "changes"}},
			"findings": {Kind: OutputArtifact},
		}, true
	case "security-reviewer":
		return Outputs{
			"findings": {Kind: OutputCount},
			"report":   {Kind: OutputArtifact},
		}, true
	case "architect":
		return Outputs{
			"plan": {Kind: OutputArtifact},
		}, true
	case "plan-executor":
		return Outputs{}, true
	case "researcher":
		return Outputs{}, true
	default:
		return nil, false
	}
}

// shippedAgentNames lists the shipped agents in sorted order.
var shippedAgentNames = []string{
	"architect",
	"plan-executor",
	"researcher",
	"reviewer",
	"security-reviewer",
}

// SuggestOutputs finds the first shipped agent (sorted by name) whose declared
// outputs contain name with a kind that fits the match (one-of containing value;
// count for >0/=0), and returns its whole Outputs.
func SuggestOutputs(name, value string, count bool) (Outputs, bool) {
	for _, agent := range shippedAgentNames {
		outs, ok := ShippedOutputs(agent)
		if !ok {
			continue
		}
		out, exists := outs[name]
		if !exists {
			continue
		}
		if count || (value == "0" && out.Kind == OutputCount) {
			if out.Kind == OutputCount {
				return outs, true
			}
		}
		if !count && out.Kind == OutputOneOf && slices.Contains(out.Values, value) {
			return outs, true
		}
	}
	return nil, false
}
