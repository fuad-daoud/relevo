package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
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

// TestMemberNamesBuilderKeepsChainName pins the shipped default's names: the
// workflow's single writer takes the chain's own name, and its reviewer,
// planner and security members take the suffixes a legacy chain always wrote.
func TestMemberNamesBuilderKeepsChainName(t *testing.T) {
	rt, _ := chainRuntime(t)
	got, err := chainMemberNames("shop", workflow.Default(), rt.RoleRegistry().WorkflowActors())
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

// TestChainNameCapFromLongestMemberSuffix pins the cap: the binding name cap
// less the longest member suffix, so the default's "-plan" caps a chain at 27
// and a custom workflow running lite-planner caps at 19.
func TestChainNameCapFromLongestMemberSuffix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		chainName string
		members   []plannedMember
		want      int
	}{
		{"the default's -plan sets 27", "shop", []plannedMember{{Name: "shop"}, {Name: "shop-rev"}, {Name: "shop-plan"}, {Name: "shop-sec"}}, 27},
		{"lite-planner sets 19", "shop", []plannedMember{{Name: "shop"}, {Name: "shop-lite-planner"}, {Name: "shop-reviewer"}}, 19},
		{"dotted child name with reviewer suffix sets 23", "shop.1", []plannedMember{{Name: "shop.1"}, {Name: "shop.1-reviewer"}}, 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := chainNameCap(tc.chainName, tc.members); got != tc.want {
				t.Errorf("chainNameCap(%q, %v) = %d, want %d", tc.chainName, tc.members, got, tc.want)
			}
		})
	}
}

// TestChainNameCapCountsLongestSuffixForChild pins that a dotted child chain name
// counts the longest suffix beyond its own name.
func TestChainNameCapCountsLongestSuffixForChild(t *testing.T) {
	members := []plannedMember{
		{Name: "shop.1"},
		{Name: "shop.1-reviewer"},
	}
	want := store.MaxAgentNameLen - len("-reviewer")
	if got := chainNameCap("shop.1", members); got != want {
		t.Errorf("chainNameCap(shop.1) = %d, want %d", got, want)
	}
}
