package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestHousekeepDepsRoundCPUs pins the wiring seam: the scope template's
// round_cpus reaches housekeep's Deps, 0 with scopes off.
func TestHousekeepDepsRoundCPUs(t *testing.T) {
	t.Parallel()

	if got := HousekeepDeps(Runtime{Scope: &spawn.ScopeSpec{RoundCPUs: 3}}).RoundCPUs; got != 3 {
		t.Errorf("RoundCPUs = %d, want the scope template's 3", got)
	}
	if got := HousekeepDeps(Runtime{}).RoundCPUs; got != 0 {
		t.Errorf("RoundCPUs = %d, want 0 with scopes off", got)
	}
}
