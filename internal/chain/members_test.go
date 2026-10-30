package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// memberNames maps a member list to name->actor for the tests that only care
// which member takes which name.
func memberNames(t *testing.T, members []PlannedMember) map[string]string {
	t.Helper()
	out := make(map[string]string, len(members))
	for _, m := range members {
		out[m.Name] = m.Actor
	}
	return out
}

// testWorkflowActors is the actor map the shipped roles define, built directly
// so these pins run without the engine's runtime: builder is the workflow's
// single writer, and lite-planner, reviewer and security are readers.
func testWorkflowActors() map[string]workflow.ActorInfo {
	return map[string]workflow.ActorInfo{
		"builder":      {Shape: workflow.ShapeWriter},
		"lite-planner": {Shape: workflow.ShapeReader},
		"reviewer":     {Shape: workflow.ShapeReader},
		"security":     {Shape: workflow.ShapeReader},
	}
}

// TestMemberNamesBuilderKeepsChainName pins the shipped default's names: the
// workflow's single writer takes the chain's own name, and its reviewer,
// planner and security members take the suffixes a legacy chain always wrote.
func TestMemberNamesBuilderKeepsChainName(t *testing.T) {
	got, err := MemberNames("shop", workflow.Default(), testWorkflowActors())
	if err != nil {
		t.Fatalf("chainMemberNames: %v", err)
	}
	names := memberNames(t, got)
	want := map[string]string{
		"shop":      "builder",
		"shop-rev":  "reviewer",
		"shop-plan": "lite-planner",
		"shop-sec":  "security",
	}
	for name, actor := range want {
		if names[name] != actor {
			t.Errorf("member %q = %q, want %q", name, names[name], actor)
		}
	}
	if len(names) != len(want) {
		t.Errorf("members = %v, want %v", names, want)
	}
}

// TestMemberNamesNoWriter pins Q1's no-writer case: with no writer actor every
// member takes "<chain>-<actor>", and the chain's own name stays free.
func TestMemberNamesNoWriter(t *testing.T) {
	def := workflow.Definition{
		Name:   "review-only",
		Params: map[string]workflow.Param{},
		Start:  "review",
		Steps: map[string]workflow.Step{
			"review": {Run: "reviewer", On: map[string]workflow.Target{"done": workflow.DoneTarget()}},
		},
	}
	got, err := MemberNames("shop", def, testWorkflowActors())
	if err != nil {
		t.Fatalf("chainMemberNames: %v", err)
	}
	names := memberNames(t, got)
	if names["shop-reviewer"] != "reviewer" {
		t.Errorf("members = %v, want shop-reviewer -> reviewer", names)
	}
	if _, reserved := names["shop"]; reserved {
		t.Errorf("members = %v, want no member on the chain's own name", names)
	}
}
