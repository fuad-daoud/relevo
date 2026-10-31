package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestNameCapFromLongestMemberSuffix pins the cap: the binding name cap
// less the longest member suffix, so the default's "-plan" caps a chain at 27
// and a custom workflow running lite-planner caps at 19.
func TestNameCapFromLongestMemberSuffix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		chainName string
		members   []PlannedMember
		want      int
	}{
		{"the default's -plan sets 27", "shop", []PlannedMember{{Name: "shop"}, {Name: "shop-rev"}, {Name: "shop-plan"}, {Name: "shop-sec"}}, 27},
		{"lite-planner sets 19", "shop", []PlannedMember{{Name: "shop"}, {Name: "shop-lite-planner"}, {Name: "shop-reviewer"}}, 19},
		{"dotted child name with reviewer suffix sets 23", "shop.1", []PlannedMember{{Name: "shop.1"}, {Name: "shop.1-reviewer"}}, 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NameCap(tc.chainName, tc.members); got != tc.want {
				t.Errorf("NameCap(%q, %v) = %d, want %d", tc.chainName, tc.members, got, tc.want)
			}
		})
	}
}

// TestNameCapCountsLongestSuffixForChild pins that a dotted child chain name
// counts the longest suffix beyond its own name.
func TestNameCapCountsLongestSuffixForChild(t *testing.T) {
	members := []PlannedMember{
		{Name: "shop.1"},
		{Name: "shop.1-reviewer"},
	}
	want := store.MaxAgentNameLen - len("-reviewer")
	if got := NameCap("shop.1", members); got != want {
		t.Errorf("NameCap(shop.1) = %d, want %d", got, want)
	}
}
