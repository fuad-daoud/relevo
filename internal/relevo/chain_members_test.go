package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// memberNames maps a member list to name->actor for the tests that only care
// which member takes which name.
func memberNames(t *testing.T, members []plannedMember) map[string]string {
	t.Helper()
	out := make(map[string]string, len(members))
	for _, m := range members {
		out[m.Name] = m.Actor
	}
	return out
}

// TestMemberNamesBuilderKeepsChainName pins Q1's common case: the workflow's
// single writer takes the chain's own name, and every reader takes
// "<chain>-<actor>".
func TestMemberNamesBuilderKeepsChainName(t *testing.T) {
	rt, _ := chainRuntime(t)
	got, err := chainMemberNames("shop", workflow.Default(), rt.RoleRegistry().WorkflowActors())
	if err != nil {
		t.Fatalf("chainMemberNames: %v", err)
	}
	names := memberNames(t, got)
	want := map[string]string{
		"shop":              "builder",
		"shop-lite-planner": "lite-planner",
		"shop-reviewer":     "reviewer",
		"shop-security":     "security",
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
	rt, _ := chainRuntime(t)
	def := workflow.Definition{
		Name:   "review-only",
		Params: map[string]workflow.Param{},
		Start:  "review",
		Steps: map[string]workflow.Step{
			"review": {Run: "reviewer", On: map[string]workflow.Target{"done": workflow.DoneTarget()}},
		},
	}
	got, err := chainMemberNames("shop", def, rt.RoleRegistry().WorkflowActors())
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

// TestChainNameCapFromLongestActor pins Q3: the cap is the binding name cap
// less the "<chain>-" prefix and the longest actor the members run, so a chain
// with lite-planner caps at 19.
func TestChainNameCapFromLongestActor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []plannedMember
		want    int
	}{
		{"lite-planner sets 19", []plannedMember{{Actor: "builder"}, {Actor: "lite-planner"}, {Actor: "reviewer"}}, 19},
		{"reviewer sets 23", []plannedMember{{Actor: "reviewer"}}, 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := chainNameCap(tc.members); got != tc.want {
				t.Errorf("chainNameCap(%v) = %d, want %d", tc.members, got, tc.want)
			}
		})
	}
}
