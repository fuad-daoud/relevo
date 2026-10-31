package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestBuilderCheckReadsTheMembersGate pins the builder member's resolved
// acceptance command: the gate of the member named builder, "" when no member
// or no gate matches.
func TestBuilderCheckReadsTheMembersGate(t *testing.T) {
	t.Parallel()

	members := []store.Binding{
		{Name: "shop", Gate: "make check"},
		{Name: "shop-rev"},
	}
	if got := BuilderCheck(members, "shop"); got != "make check" {
		t.Errorf("BuilderCheck(shop) = %q, want %q", got, "make check")
	}
	if got := BuilderCheck(members, "shop-rev"); got != "" {
		t.Errorf("BuilderCheck(gateless) = %q, want empty", got)
	}
	if got := BuilderCheck(members, "shop-plan"); got != "" {
		t.Errorf("BuilderCheck(missing) = %q, want empty", got)
	}
	if got := BuilderCheck(nil, "shop"); got != "" {
		t.Errorf("BuilderCheck(nil) = %q, want empty", got)
	}
}
