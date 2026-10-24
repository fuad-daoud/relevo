package relevo

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// TestServedChainPreflightRefusesUnsupportedTier pins the chain create's
// launch render: a builder policy tier of edit on the claude candidate cannot
// run the gate, so the preflight refuses with the harness message before
// anything exists.
func TestServedChainPreflightRefusesUnsupportedTier(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	rows := chainRows()
	rows["builder"] = roles.Row{Candidates: []string{testClaudeRef}, Tier: ptr("edit")}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)

	_, err := ServedChainPreflight(rt, servedChainRequest("shop"))
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want errors.Is(err, harness.ErrTierUnsupported)", err)
	}
	assertServedChainAbsent(t, rt, "shop")
}
