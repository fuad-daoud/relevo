package relevo

import (
	"context"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
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

// TestHousekeepDepsRoundCPUSize pins the wiring seam: the runtime's learned
// CPU size reaches housekeep's Deps, and a local runtime's nil stays nil.
func TestHousekeepDepsRoundCPUSize(t *testing.T) {
	t.Parallel()

	want := func(store.Binding) (int, bool) { return 3, true }
	if got := HousekeepDeps(Runtime{RoundCPUSize: want}).RoundCPUSize; got == nil {
		t.Fatal("RoundCPUSize = nil, want the runtime's seam copied")
	} else if n, ok := got(store.Binding{}); !ok || n != 3 {
		t.Errorf("RoundCPUSize() = %d, %v; want 3, true", n, ok)
	}
	if got := HousekeepDeps(Runtime{}).RoundCPUSize; got != nil {
		t.Error("RoundCPUSize != nil, want nil on a local runtime")
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

// TestStartRoundPinsTheLearnedCPUSize pins the user's entry: a served round
// start calls AssignRoundCPU through HousekeepDeps, so the learned seam
// answers the size the saved binding gets -- three cores with a stub
// answering 3, the configured 2 when the stub answers not ok. The topology is
// faked, so no test reads the host's sysfs.
func TestStartRoundPinsTheLearnedCPUSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		seam func(store.Binding) (int, bool)
		want []int
	}{
		{"learned 3", func(store.Binding) (int, bool) { return 3, true }, []int{0, 1, 2}},
		{"learned not ok keeps the configured 2", func(store.Binding) (int, bool) { return 0, false }, []int{0, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := newFakeRunner()
			rt, b := seedHeadless(t, fr)
			rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", AllowedCPUs: "0-3", RoundCPUs: 2}
			rt.CPUTopology = func([]int) spawn.CPUTopology { return spawn.CPUTopology{} }
			rt.RoundCPUSize = c.seam

			err := rt.Store.WithLock(func(tx *store.Tx) error {
				got, err := startRound(context.Background(), rt, tx, b, "p", false)
				if err != nil {
					return err
				}
				return tx.Save(got)
			})
			if err != nil {
				t.Fatalf("startRound: %v", err)
			}
			saved, err := rt.Store.Load("webshop")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(saved.RoundCPUs, c.want) {
				t.Errorf("saved RoundCPUs = %v, want %v", saved.RoundCPUs, c.want)
			}
		})
	}
}
