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

// TestHousekeepDepsCPUTopology pins the topology seam: the runtime's reader
// reaches housekeep's Deps, so a server's tests and probes pick the same sets
// on every host, and a nil reader stays nil so housekeep reads sysfs.
func TestHousekeepDepsCPUTopology(t *testing.T) {
	t.Parallel()

	want := spawn.CPUTopology{Siblings: map[int][]int{0: {0, 1}}}
	deps := HousekeepDeps(Runtime{CPUTopology: func([]int) spawn.CPUTopology { return want }})
	if deps.Topology == nil {
		t.Fatal("Topology = nil, want the runtime's reader")
	}
	if got := deps.Topology(nil); len(got.Siblings[0]) != 2 {
		t.Errorf("Topology() = %+v, want the runtime's reader's answer", got)
	}
	if HousekeepDeps(Runtime{}).Topology != nil {
		t.Error("Topology != nil with no runtime reader, want nil so housekeep reads the host")
	}
}
