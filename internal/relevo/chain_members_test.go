package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// memberNames maps a member list to name->actor for the tests that only care
// which member takes which name.
func memberNames(t *testing.T, members []chain.PlannedMember) map[string]string {
	t.Helper()
	out := make(map[string]string, len(members))
	for _, m := range members {
		out[m.Name] = m.Actor
	}
	return out
}

// TestChainNameCapFromLongestMemberSuffix pins the cap: the binding name cap
// less the longest member suffix, so the default's "-plan" caps a chain at 27
// and a custom workflow running lite-planner caps at 19.
func TestChainNameCapFromLongestMemberSuffix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		chainName string
		members   []chain.PlannedMember
		want      int
	}{
		{"the default's -plan sets 27", "shop", []chain.PlannedMember{{Name: "shop"}, {Name: "shop-rev"}, {Name: "shop-plan"}, {Name: "shop-sec"}}, 27},
		{"lite-planner sets 19", "shop", []chain.PlannedMember{{Name: "shop"}, {Name: "shop-lite-planner"}, {Name: "shop-reviewer"}}, 19},
		{"dotted child name with reviewer suffix sets 23", "shop.1", []chain.PlannedMember{{Name: "shop.1"}, {Name: "shop.1-reviewer"}}, 23},
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
	members := []chain.PlannedMember{
		{Name: "shop.1"},
		{Name: "shop.1-reviewer"},
	}
	want := store.MaxAgentNameLen - len("-reviewer")
	if got := chainNameCap("shop.1", members); got != want {
		t.Errorf("chainNameCap(shop.1) = %d, want %d", got, want)
	}
}
