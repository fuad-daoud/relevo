package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestMemberNamesCustomWorkflowUsesActorSuffix pins that the legacy suffixes
// belong to the shipped default alone: a workflow with any other name gives its
// reviewer param's actor the "<chain>-<actor>" name.
func TestMemberNamesCustomWorkflowUsesActorSuffix(t *testing.T) {
	def := workflow.Definition{
		Name:   "review-only",
		Params: map[string]workflow.Param{"reviewer": {Kind: workflow.ParamString, Str: "reviewer"}},
		Start:  "review",
		Steps:  map[string]workflow.Step{"review": {Run: "{{params.reviewer}}", On: map[string]workflow.Target{"done": workflow.DoneTarget()}}},
	}
	got, err := MemberNames("shop", def, testWorkflowActors())
	if err != nil {
		t.Fatalf("chainMemberNames: %v", err)
	}
	names := memberNames(t, got)
	if names["shop-reviewer"] != "reviewer" {
		t.Errorf("members = %v, want shop-reviewer -> reviewer", names)
	}
	if _, legacy := names["shop-rev"]; legacy {
		t.Errorf("members = %v, want no legacy name on a custom workflow", names)
	}
}

// TestMemberNamesDefaultSharedActorTakesFirstPart pins the shared-actor rule:
// when one actor fills several parts of the default, the first in reviewer,
// planner, security order names it, so it takes "-rev".
func TestMemberNamesDefaultSharedActorTakesFirstPart(t *testing.T) {
	def := workflow.Definition{
		Name: "default",
		Params: map[string]workflow.Param{
			"reviewer": {Kind: workflow.ParamString, Str: "reviewer"},
			"planner":  {Kind: workflow.ParamString, Str: "reviewer"},
		},
		Start: "review",
		Steps: map[string]workflow.Step{"review": {Run: "{{params.reviewer}}", On: map[string]workflow.Target{"done": workflow.DoneTarget()}}},
	}
	got, err := MemberNames("shop", def, testWorkflowActors())
	if err != nil {
		t.Fatalf("chainMemberNames: %v", err)
	}
	names := memberNames(t, got)
	if names["shop-rev"] != "reviewer" {
		t.Errorf("members = %v, want the shared actor to take shop-rev", names)
	}
	if _, planner := names["shop-plan"]; planner {
		t.Errorf("members = %v, want the reviewer part to win over the planner", names)
	}
}
