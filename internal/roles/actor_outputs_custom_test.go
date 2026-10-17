package roles

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestCustomAgentActorOutputsValidation(t *testing.T) {
	agents := map[string]AgentEntry{
		"morphic-reviewer": {
			Shape: "reader",
			Native: map[string]DefRow{
				"claude": {Agent: "morphic-reviewer"},
			},
		},
	}
	actorsWithOutputs := map[string]Actor{
		"builder":  {Agent: "plan-executor"},
		"reviewer": {Agent: "reviewer"},
		"morphic-reviewer": {
			Agent: "morphic-reviewer",
			Outputs: workflow.Outputs{
				"verdict":  {Kind: workflow.OutputOneOf, Values: []string{"pass", "changes"}},
				"findings": {Kind: workflow.OutputArtifact},
			},
		},
		"lite-planner": {Agent: "architect"},
		"planner":      {Agent: "architect"},
		"security":     {Agent: "security-reviewer"},
	}

	f, _, err := FromActors(agents, actorsWithOutputs)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}
	reg, err := Build(f, &candidate.Set{}, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	defWithOutputs, err := workflow.WithParams(workflow.Default(), map[string]string{"reviewer": "morphic-reviewer"})
	if err != nil {
		t.Fatalf("WithParams: %v", err)
	}

	env := workflow.Env{
		Actors: reg.WorkflowActors(),
		Given:  &workflow.Given{Plans: true},
		Seeds:  workflow.ShippedSeeds(),
	}
	problems := workflow.Validate(defWithOutputs, env)
	if len(problems) != 0 {
		t.Fatalf("Validate with outputs: want 0 problems, got %d: %v", len(problems), problems)
	}

	// Without outputs:
	actorsNoOutputs := map[string]Actor{
		"builder":          {Agent: "plan-executor"},
		"reviewer":         {Agent: "reviewer"},
		"morphic-reviewer": {Agent: "morphic-reviewer"},
		"lite-planner":     {Agent: "architect"},
		"planner":          {Agent: "architect"},
		"security":         {Agent: "security-reviewer"},
	}
	f2, _, err := FromActors(agents, actorsNoOutputs)
	if err != nil {
		t.Fatalf("FromActors (no outputs): %v", err)
	}
	reg2, err := Build(f2, &candidate.Set{}, policy.Policy{})
	if err != nil {
		t.Fatalf("Build (no outputs): %v", err)
	}

	defNoOutputs, err := workflow.WithParams(workflow.Default(), map[string]string{"reviewer": "morphic-reviewer"})
	if err != nil {
		t.Fatalf("WithParams: %v", err)
	}

	env2 := workflow.Env{
		Actors: reg2.WorkflowActors(),
		Given:  &workflow.Given{Plans: true},
		Seeds:  workflow.ShippedSeeds(),
	}
	problems2 := workflow.Validate(defNoOutputs, env2)
	var reviewHints []string
	for _, p := range problems2 {
		s := p.String()
		if p.Step == "review" && p.Rule == workflow.RuleMatch && strings.Contains(s, "declares no outcome") {
			reviewHints = append(reviewHints, s)
		}
	}
	if len(reviewHints) != 1 {
		t.Fatalf("Validate without outputs: want 1 review hint, got %d: %v", len(reviewHints), reviewHints)
	}
	wantPrefix := `step review: rule 3: actor "morphic-reviewer" declares no outcome "verdict"`
	if !strings.HasPrefix(reviewHints[0], wantPrefix) {
		t.Fatalf("review hint = %q, want prefix %q", reviewHints[0], wantPrefix)
	}
}
