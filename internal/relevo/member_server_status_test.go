package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestChainStatusThroughMemberNameShowsLocalRowsAndTheServerFailure pins the
// member read on a chain that runs on a server: the resolution a member-named
// status runs lands on the chain's own report, whose row carries the server
// failure on its detail while every member row stays this machine's own.
func TestChainStatusThroughMemberNameShowsLocalRowsAndTheServerFailure(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{getChainErr: errors.New("dial tcp: connection refused")}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	// The two-step read cmdStatus runs for a member name.
	c, ok := MemberChain(rt.Store.ChainByMember, "shop-plan")
	if !ok || c.Name != "shop" {
		t.Fatalf("MemberChain(shop-plan) = %s/%v, want the chain shop", c.Name, ok)
	}
	rep, err := ChainStatus(context.Background(), rt, c.Name)
	if err != nil {
		t.Fatalf("ChainStatus: %v", err)
	}

	rows := rep.Bindings
	if len(rows) != 4 {
		t.Fatalf("rows = %v, want the chain and its three members", rowNames(rep))
	}
	if rows[0].Name != "shop" || rows[0].Chain == nil {
		t.Fatalf("first row = %q (chain %v), want the chain row", rows[0].Name, rows[0].Chain != nil)
	}
	if detail := rows[0].Detail; !strings.Contains(detail, "server zen unreachable") {
		t.Errorf("chain row detail = %q, want the unreadable server named", detail)
	}
	for i, want := range []string{"shop", "shop-rev", "shop-plan"} {
		if rows[1+i].Name != want || rows[1+i].Chain != nil {
			t.Errorf("member row %d = %q (chain %v), want the local %q as an ordinary row",
				1+i, rows[1+i].Name, rows[1+i].Chain != nil, want)
		}
	}
}
