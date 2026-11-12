package housekeep

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/store/storetest"
)

// TestPickCPUSet pins the allocator: the least-used size-sized set of pool
// cores, ties to the lowest cores, with no topology; not ok when the pool
// is empty or size is out of range, while a full pool shares the least-held
// set. held is a multiset: duplicates raise a core's count, and cores
// outside the pool are ignored.
func TestPickCPUSet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		pool []int
		held []int
		size int
		want []int
		ok   bool
	}{
		{"empty pool", nil, nil, 1, nil, false},
		{"size zero", []int{0, 1, 2}, nil, 0, nil, false},
		{"size exceeds the pool", []int{0, 1}, nil, 3, nil, false},
		{"nothing held", []int{0, 1, 2}, nil, 1, []int{0}, true},
		{"first held", []int{0, 1, 2}, []int{0}, 1, []int{1}, true},
		{"first and last held", []int{0, 1, 2}, []int{0, 2}, 1, []int{1}, true},
		{"all held shares the lowest core", []int{0, 1, 2}, []int{0, 1, 2}, 1, []int{0}, true},
		{"all held with duplicates shares the set", []int{0, 1}, []int{0, 1, 1, 0}, 2, []int{0, 1}, true},
		{"held outside the pool", []int{0, 1, 2}, []int{5, 0}, 1, []int{1}, true},
		{"ties go to the lowest cores", []int{0, 1, 2, 3}, nil, 2, []int{0, 1}, true},
		{"counts break ties", []int{0, 1, 2, 3}, []int{0, 0, 1}, 2, []int{2, 3}, true},
		{"size three skips the hottest core", []int{0, 1, 2, 3}, []int{3, 3, 3, 0}, 3, []int{0, 1, 2}, true},
		{"duplicates are counts", []int{0, 1, 2}, []int{0, 0, 1}, 2, []int{1, 2}, true},
		{"a held core joins only a set with no free room", []int{0, 1}, []int{0}, 2, []int{0, 1}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := pickCPUSet(c.pool, c.held, c.size, spawn.CPUTopology{})
			if ok != c.ok || !reflect.DeepEqual(got, c.want) {
				t.Errorf("pickCPUSet(%v, %v, %d) = %v, %v; want %v, %v", c.pool, c.held, c.size, got, ok, c.want, c.ok)
			}
		})
	}
}

// homeTopology is the paired-thread host: siblings 0-1 and 2-3 on the fast
// cluster, every other core alone on the slow one.
func homeTopology() spawn.CPUTopology {
	topo := spawn.CPUTopology{
		Siblings:   map[int][]int{0: {0, 1}, 1: {0, 1}, 2: {2, 3}, 3: {2, 3}},
		MaxFreqKHz: make(map[int]int, 12),
	}
	for c := 0; c < 12; c++ {
		topo.MaxFreqKHz[c] = 3300000
	}
	for _, c := range []int{0, 1, 2, 3} {
		topo.MaxFreqKHz[c] = 4400000
	}
	return topo
}

// crossTopology pairs each low core with a high one, all at one speed.
func crossTopology() spawn.CPUTopology {
	topo := spawn.CPUTopology{Siblings: map[int][]int{}, MaxFreqKHz: map[int]int{}}
	for c := 0; c < 8; c++ {
		topo.MaxFreqKHz[c] = 4200000
	}
	for _, p := range [][2]int{{0, 4}, {1, 5}, {2, 6}, {3, 7}} {
		topo.Siblings[p[0]] = []int{p[0], p[1]}
		topo.Siblings[p[1]] = []int{p[0], p[1]}
	}
	return topo
}

// TestPickCPUSetTopologySequences pins the round order: sets spread across
// physical cores fastest-first, and a full pool shares the least-held set
// pinned. Rounds run one after another with every picked set staying held.
func TestPickCPUSetTopologySequences(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		pool []int
		size int
		topo spawn.CPUTopology
		want [][]int
	}{
		{"home spreads across cores then shares", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 2, homeTopology(),
			[][]int{{0, 2}, {4, 5}, {6, 7}, {8, 9}, {10, 11}, {1, 3}, {4, 5}}},
		{"cross pairs spread then share", []int{0, 1, 2, 3, 4, 5, 6, 7}, 2, crossTopology(),
			[][]int{{0, 1}, {2, 3}, {4, 5}, {6, 7}, {0, 1}}},
		{"alone cores share the lowest pair", []int{0, 1, 2, 3}, 2, spawn.CPUTopology{},
			[][]int{{0, 1}, {2, 3}, {0, 1}}},
		{"no topology matches the old order", []int{0, 1, 2, 3}, 2, spawn.CPUTopology{},
			[][]int{{0, 1}, {2, 3}}},
		{"the faster core wins a free tie", []int{0, 1}, 1,
			spawn.CPUTopology{MaxFreqKHz: map[int]int{0: 1000000, 1: 2000000}}, [][]int{{1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var held []int
			for i, want := range c.want {
				got, ok := pickCPUSet(c.pool, held, c.size, c.topo)
				if !ok || !reflect.DeepEqual(got, want) {
					t.Fatalf("round %d: pickCPUSet(%v, %v, %d) = %v, %v; want %v, true", i+1, c.pool, held, c.size, got, ok, want)
				}
				held = append(held, got...)
			}
		})
	}
}

// TestHeldIn pins the census rule: self is excluded; a binding with a
// RoundCPU but no live process and no gate blocks nothing; a nil RoundCPU is
// excluded even with a live process or a running gate.
func TestHeldIn(t *testing.T) {
	t.Parallel()

	zero, one := 0, 1
	bindings := []store.Binding{
		{Name: "self", RoundCPU: &one, Builder: store.Endpoint{PID: 42}},                            // excluded: self
		{Name: "live", RoundCPU: &zero, Builder: store.Endpoint{PID: 42}},                           // included
		{Name: "gated", RoundCPU: &one, GateRun: &store.GateRun{PID: 7}},                            // included: gate running
		{Name: "checked", RoundCPU: &zero, CheckRun: &store.CheckRun{ID: "c1"}},                     // included: served check running
		{Name: "checked-done", RoundCPU: &one, CheckRun: &store.CheckRun{ID: "c2", Result: "pass"}}, // excluded: check settled
		{Name: "dead", RoundCPU: &one},                                                              // excluded: PID 0, no gate
		{Name: "unpinned", Builder: store.Endpoint{PID: 42}},                                        // excluded: nil RoundCPU
		{Name: "gated-unpinned", GateRun: &store.GateRun{PID: 7}},                                   // excluded: nil RoundCPU
	}
	want := []int{0, 1, 0}
	if got := HeldIn(bindings, "self"); !reflect.DeepEqual(got, want) {
		t.Errorf("HeldIn = %v, want %v", got, want)
	}
}

// TestHeldInWithLists pins the set census: a live round contributes every
// core of its list, a legacy single still contributes its core, and
// duplicates across rounds are kept as the per-core counts.
func TestHeldInWithLists(t *testing.T) {
	t.Parallel()

	two := 2
	bindings := []store.Binding{
		{Name: "self", RoundCPUs: []int{0, 1}, Builder: store.Endpoint{PID: 42}}, // excluded: self
		{Name: "listed", RoundCPUs: []int{0, 1}, Builder: store.Endpoint{PID: 42}},
		{Name: "gated-list", RoundCPUs: []int{1, 2}, GateRun: &store.GateRun{PID: 7}},
		{Name: "legacy", RoundCPU: &two, Builder: store.Endpoint{PID: 42}},
		{Name: "dead-list", RoundCPUs: []int{5}},
		{Name: "unpinned", Builder: store.Endpoint{PID: 42}},
	}
	want := []int{0, 1, 1, 2, 2}
	if got := HeldIn(bindings, "self"); !reflect.DeepEqual(got, want) {
		t.Errorf("HeldIn = %v, want %v", got, want)
	}
}

// TestHeldInChecks pins the chain-check census: a binding named in the map
// holds its set even with no live round of its own, while self stays
// excluded and an empty map reads exactly as HeldIn.
func TestHeldInChecks(t *testing.T) {
	t.Parallel()

	bindings := []store.Binding{
		{Name: "self", RoundCPUs: []int{0}, Builder: store.Endpoint{PID: 42}}, // excluded: self, even checked
		{Name: "member", RoundCPUs: []int{0, 1}},                              // included: named, though idle
		{Name: "live", RoundCPUs: []int{2}, Builder: store.Endpoint{PID: 42}}, // included: live
		{Name: "dead", RoundCPUs: []int{3}},                                   // excluded: idle and unnamed
	}
	checks := map[string]int{"member": 4321, "self": 4322}
	if got, want := HeldInChecks(bindings, "self", checks), []int{0, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("HeldInChecks = %v, want %v", got, want)
	}
	if got, want := HeldInChecks(bindings, "self", nil), HeldIn(bindings, "self"); !reflect.DeepEqual(got, want) {
		t.Errorf("HeldInChecks(nil) = %v, want HeldIn %v", got, want)
	}
}

// TestLocalHeldCPUs reads a real temp store's bindings through the caller's
// transaction, the local census path round starts use.
func TestLocalHeldCPUs(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	zero, one := 0, 1
	err := st.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(store.Binding{Name: "self", CWD: "/repo-self", RoundCPU: &one, Builder: store.Endpoint{PID: 11}}); err != nil {
			return err
		}
		if err := tx.Save(store.Binding{Name: "other", CWD: "/repo-other", RoundCPU: &zero, Builder: store.Endpoint{PID: 22}}); err != nil {
			return err
		}
		got, err := localHeldCPUs(tx, "self")
		if err != nil {
			return err
		}
		if want := []int{0}; !reflect.DeepEqual(got, want) {
			t.Errorf("localHeldCPUs = %v, want %v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}

// TestRoundCPUJSON pins the field shape: core 0 is a valid pin and survives
// a JSON round trip, a binding JSON without the key decodes to nil, and a nil
// pin is omitted.
func TestRoundCPUJSON(t *testing.T) {
	t.Parallel()

	zero := 0
	data, err := json.Marshal(store.Binding{Name: "x", RoundCPU: &zero})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got store.Binding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.RoundCPU == nil || *got.RoundCPU != 0 {
		t.Fatalf("RoundCPU after a JSON round trip = %v, want 0", got.RoundCPU)
	}

	var old store.Binding
	if err := json.Unmarshal([]byte(`{"name":"y"}`), &old); err != nil {
		t.Fatalf("Unmarshal without the key: %v", err)
	}
	if old.RoundCPU != nil {
		t.Fatalf("a binding JSON without round_cpu decoded to %v, want nil", old.RoundCPU)
	}

	plain, err := json.Marshal(store.Binding{Name: "z"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(plain), "round_cpu") {
		t.Errorf("a nil RoundCPU was written: %s", plain)
	}
}

// TestCPUPinText pins the pin string: "" when no core is pinned, the decimal
// core otherwise -- core 0 included.
func TestCPUPinText(t *testing.T) {
	t.Parallel()

	zero, two := 0, 2
	cases := []struct {
		name string
		b    store.Binding
		want string
	}{
		{"no pin", store.Binding{}, ""},
		{"core 0", store.Binding{RoundCPU: &zero}, "0"},
		{"core 2", store.Binding{RoundCPU: &two}, "2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CPUPinText(c.b); got != c.want {
				t.Errorf("CPUPinText = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRoundPinText pins the set pin string: the comma-joined list when set,
// the single-core pin for a legacy binding, "" when neither is.
func TestRoundPinText(t *testing.T) {
	t.Parallel()

	zero := 0
	cases := []struct {
		name string
		b    store.Binding
		want string
	}{
		{"no pin", store.Binding{}, ""},
		{"legacy core 0", store.Binding{RoundCPU: &zero}, "0"},
		{"pair", store.Binding{RoundCPUs: []int{0, 2}}, "0,2"},
		{"list wins over the single", store.Binding{RoundCPU: &zero, RoundCPUs: []int{1, 2}}, "1,2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RoundPinText(c.b); got != c.want {
				t.Errorf("RoundPinText = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRoundCPUsJSON pins the list field shape: cores survive a JSON round
// trip, and a nil list is omitted.
func TestRoundCPUsJSON(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(store.Binding{Name: "x", RoundCPUs: []int{0, 2}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got store.Binding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.RoundCPUs, []int{0, 2}) {
		t.Fatalf("RoundCPUs after a JSON round trip = %v, want [0 2]", got.RoundCPUs)
	}

	plain, err := json.Marshal(store.Binding{Name: "z"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(plain), "round_cpus") {
		t.Errorf("a nil RoundCPUs was written: %s", plain)
	}
}

// cpuPtrText names a pinned core for a test failure message: "none" for nil.
func cpuPtrText(p *int) string {
	if p == nil {
		return "none"
	}
	return strconv.Itoa(*p)
}

// cpuTestDeps is a Deps with the pool pinned to the list and the census left
// to the caller's transaction.
func cpuTestDeps(st *store.Store, pool string) Deps {
	return Deps{Topology: noTopology, Store: st, AllowedCPUs: pool}
}

// saveCPUBinding saves a plain binding the allocator can pin.
func saveCPUBinding(t *testing.T, st *store.Store, name string) {
	t.Helper()
	if err := st.Save(store.Binding{Name: name, CWD: "/repo-" + name}); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
}

// TestAssignRoundCPUGivesDistinctCores pins the allocation: with pool "0-1",
// the first round takes the lowest free core and, while it is live, the next
// takes the next one.
func TestAssignRoundCPUGivesDistinctCores(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "second")
	deps := cpuTestDeps(st, "0-1")

	var first, second store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		first = AssignRoundCPU(deps, tx, b)
		first.Builder.PID = 4001
		if err := tx.Save(first); err != nil {
			return err
		}
		b2, err := tx.Load("second")
		if err != nil {
			return err
		}
		second = AssignRoundCPU(deps, tx, b2)
		return tx.Save(second)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if first.RoundCPU == nil || *first.RoundCPU != 0 {
		t.Errorf("first RoundCPU = %s, want 0", cpuPtrText(first.RoundCPU))
	}
	if second.RoundCPU == nil || *second.RoundCPU != 1 {
		t.Errorf("second RoundCPU = %s, want 1", cpuPtrText(second.RoundCPU))
	}
}

// TestAssignRoundCPUFullPoolSharesPinned pins the sharing rule: when
// the only pool core is already held, the round shares it pinned instead of
// running on the whole pool.
func TestAssignRoundCPUFullPoolSharesPinned(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "second")
	deps := cpuTestDeps(st, "0")

	var second store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		first, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		first = AssignRoundCPU(deps, tx, first)
		first.Builder.PID = 4001
		if err := tx.Save(first); err != nil {
			return err
		}
		b2, err := tx.Load("second")
		if err != nil {
			return err
		}
		second = AssignRoundCPU(deps, tx, b2)
		return tx.Save(second)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if !reflect.DeepEqual(second.RoundCPUs, []int{0}) {
		t.Errorf("a full pool left RoundCPUs = %v, want the shared set [0]", second.RoundCPUs)
	}
	if second.RoundCPU == nil || *second.RoundCPU != 0 {
		t.Errorf("a full pool left RoundCPU = %s, want the shared core 0", cpuPtrText(second.RoundCPU))
	}
}

// TestAssignRoundCPUKeepsCoreOnRelaunch pins the relaunch rule: a binding
// whose RoundCPU is still in the pool and still free keeps it.
func TestAssignRoundCPUKeepsCoreOnRelaunch(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := cpuTestDeps(st, "0-3")

	one := 1
	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.RoundCPU = &one
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if got.RoundCPU == nil || *got.RoundCPU != 1 {
		t.Errorf("RoundCPU = %s, want the kept core 1", cpuPtrText(got.RoundCPU))
	}
}

// TestAssignRoundCPUReusesReleasedCore pins the release: once a live round's
// pin is cleared, the next round on another binding can take that core.
func TestAssignRoundCPUReusesReleasedCore(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "second")
	saveCPUBinding(t, st, "third")
	deps := cpuTestDeps(st, "0-1")

	var second, third store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		first, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		first = AssignRoundCPU(deps, tx, first)
		first.Builder.PID = 4001
		if err := tx.Save(first); err != nil {
			return err
		}
		b2, err := tx.Load("second")
		if err != nil {
			return err
		}
		second = AssignRoundCPU(deps, tx, b2)
		second.Builder.PID = 4002
		if err := tx.Save(second); err != nil {
			return err
		}
		// The first round closes: its pin and its process go with it.
		first.RoundCPU = nil
		first.Builder.PID = 0
		if err := tx.Save(first); err != nil {
			return err
		}
		b3, err := tx.Load("third")
		if err != nil {
			return err
		}
		third = AssignRoundCPU(deps, tx, b3)
		return tx.Save(third)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if second.RoundCPU == nil || *second.RoundCPU != 1 {
		t.Fatalf("second RoundCPU = %s, want 1", cpuPtrText(second.RoundCPU))
	}
	if third.RoundCPU == nil || *third.RoundCPU != 0 {
		t.Errorf("third RoundCPU = %s, want the released core 0", cpuPtrText(third.RoundCPU))
	}
}

// TestAssignRoundCPUNoPoolWritesNoField pins the off switch: a pool with no
// allowed_cpus leaves RoundCPU nil.
func TestAssignRoundCPUNoPoolWritesNoField(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := cpuTestDeps(st, "")

	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if got.RoundCPU != nil {
		t.Errorf("RoundCPU = %s, want none with no pool", cpuPtrText(got.RoundCPU))
	}
}

// cpuTestSetDeps is a Deps with the pool and the set size pinned.
func cpuTestSetDeps(st *store.Store, pool string, size int) Deps {
	return Deps{Topology: noTopology, Store: st, AllowedCPUs: pool, RoundCPUs: size}
}

// fakeOnlineCPUs pins the machine to n CPUs for one test. Tests that fake it
// stay sequential: parallel tests read the real count.
func fakeOnlineCPUs(t *testing.T, n int) {
	t.Helper()
	old := onlineCPUs
	onlineCPUs = func() int { return n }
	t.Cleanup(func() { onlineCPUs = old })
}

// TestAssignRoundCPUGivesDistinctSets pins set allocation: with pool "0-3"
// and size 2, the first live round takes the lowest pair and the next takes
// the next pair, each with its single pin on the set's first core.
func TestAssignRoundCPUGivesDistinctSets(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "second")
	deps := cpuTestSetDeps(st, "0-3", 2)

	var first, second store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		first = AssignRoundCPU(deps, tx, b)
		first.Builder.PID = 4001
		if err := tx.Save(first); err != nil {
			return err
		}
		b2, err := tx.Load("second")
		if err != nil {
			return err
		}
		second = AssignRoundCPU(deps, tx, b2)
		return tx.Save(second)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if !reflect.DeepEqual(first.RoundCPUs, []int{0, 1}) {
		t.Errorf("first RoundCPUs = %v, want [0 1]", first.RoundCPUs)
	}
	if first.RoundCPU == nil || *first.RoundCPU != 0 {
		t.Errorf("first RoundCPU = %s, want the set's first core 0", cpuPtrText(first.RoundCPU))
	}
	if !reflect.DeepEqual(second.RoundCPUs, []int{2, 3}) {
		t.Errorf("second RoundCPUs = %v, want [2 3]", second.RoundCPUs)
	}
	if second.RoundCPU == nil || *second.RoundCPU != 2 {
		t.Errorf("second RoundCPU = %s, want the set's first core 2", cpuPtrText(second.RoundCPU))
	}
}

// TestAssignRoundCPUFullSetSharesAndReplaces pins the set sharing rule: when
// no unheld core is left, the round shares the least-held set pinned, and a
// stale set on the binding is replaced.
func TestAssignRoundCPUFullSetSharesAndReplaces(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "second")
	deps := cpuTestSetDeps(st, "0-1", 2)

	var second store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		first, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		first = AssignRoundCPU(deps, tx, first)
		first.Builder.PID = 4001
		if err := tx.Save(first); err != nil {
			return err
		}
		b2, err := tx.Load("second")
		if err != nil {
			return err
		}
		// The second round arrives with a stale set on the cores the first
		// live round already holds, so the full pool shares them again.
		stale := 0
		b2.RoundCPUs = []int{0, 1}
		b2.RoundCPU = &stale
		second = AssignRoundCPU(deps, tx, b2)
		return tx.Save(second)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if !reflect.DeepEqual(second.RoundCPUs, []int{0, 1}) {
		t.Errorf("a full pool left RoundCPUs = %v, want the shared set [0 1]", second.RoundCPUs)
	}
	if second.RoundCPU == nil || *second.RoundCPU != 0 {
		t.Errorf("a full pool left RoundCPU = %s, want the shared set's first core 0", cpuPtrText(second.RoundCPU))
	}
}

// TestAssignRoundCPUTopologySpreadsAcrossCores pins the entry path: with the
// home topology faked in, a pool of "0-11" at size 2 gives the first live
// round {0,2} and the next {4,5}.
func TestAssignRoundCPUTopologySpreadsAcrossCores(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "second")
	deps := cpuTestSetDeps(st, "0-11", 2)
	deps.Topology = func([]int) spawn.CPUTopology { return homeTopology() }

	var first, second store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		first = AssignRoundCPU(deps, tx, b)
		first.Builder.PID = 4001
		if err := tx.Save(first); err != nil {
			return err
		}
		b2, err := tx.Load("second")
		if err != nil {
			return err
		}
		second = AssignRoundCPU(deps, tx, b2)
		return tx.Save(second)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if !reflect.DeepEqual(first.RoundCPUs, []int{0, 2}) {
		t.Errorf("first RoundCPUs = %v, want [0 2]", first.RoundCPUs)
	}
	if first.RoundCPU == nil || *first.RoundCPU != 0 {
		t.Errorf("first RoundCPU = %s, want the set's first core 0", cpuPtrText(first.RoundCPU))
	}
	if !reflect.DeepEqual(second.RoundCPUs, []int{4, 5}) {
		t.Errorf("second RoundCPUs = %v, want [4 5]", second.RoundCPUs)
	}
	if second.RoundCPU == nil || *second.RoundCPU != 4 {
		t.Errorf("second RoundCPU = %s, want the set's first core 4", cpuPtrText(second.RoundCPU))
	}
}

// TestAssignRoundCPUCensusErrorClearsStaleSet pins the census-error rule: a
// stale set on the binding is cleared when the held-cores census fails, so
// the round runs unpinned rather than on a set no census vetted.
func TestAssignRoundCPUCensusErrorClearsStaleSet(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := Deps{Topology: noTopology, Store: st, AllowedCPUs: "0-3", RoundCPUs: 2,
		HeldCPUs: func(*store.Tx, string) ([]int, error) { return nil, errors.New("census down") }}

	stale := 0
	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.RoundCPUs = []int{0, 1}
		b.RoundCPU = &stale
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if got.RoundCPUs != nil {
		t.Errorf("a failed census left RoundCPUs = %v, want none", got.RoundCPUs)
	}
	if got.RoundCPU != nil {
		t.Errorf("a failed census left RoundCPU = %s, want none", cpuPtrText(got.RoundCPU))
	}
}

// TestAssignRoundCPUNilTxClearsStaleSet pins the nil-tx rule: a caller with
// no round to pin clears a stale set, so the round runs unpinned.
func TestAssignRoundCPUNilTxClearsStaleSet(t *testing.T) {
	t.Parallel()

	stale := 0
	b := store.Binding{Name: "webshop", RoundCPUs: []int{0, 1}, RoundCPU: &stale}
	got := AssignRoundCPU(Deps{Topology: noTopology, AllowedCPUs: "0-3", RoundCPUs: 2}, nil, b)

	if got.RoundCPUs != nil {
		t.Errorf("a nil tx left RoundCPUs = %v, want none", got.RoundCPUs)
	}
	if got.RoundCPU != nil {
		t.Errorf("a nil tx left RoundCPU = %s, want none", cpuPtrText(got.RoundCPU))
	}
}

// TestAssignRoundCPUKeepsSetOnRelaunch pins the set relaunch rule: a stored
// set still entirely in the pool and entirely free survives.
func TestAssignRoundCPUKeepsSetOnRelaunch(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := cpuTestSetDeps(st, "0-3", 2)

	one := 1
	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.RoundCPUs = []int{1, 2}
		b.RoundCPU = &one
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !reflect.DeepEqual(got.RoundCPUs, []int{1, 2}) {
		t.Errorf("RoundCPUs = %v, want the kept set [1 2]", got.RoundCPUs)
	}
	if got.RoundCPU == nil || *got.RoundCPU != 1 {
		t.Errorf("RoundCPU = %s, want the kept core 1", cpuPtrText(got.RoundCPU))
	}
}

// TestAssignRoundCPURePicksWhenStoredSetHeld pins the relaunch-held rule: a
// stored set with a core another live round holds is re-picked, not kept.
func TestAssignRoundCPURePicksWhenStoredSetHeld(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "holder")
	deps := cpuTestSetDeps(st, "0-3", 2)

	one := 1
	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		holder, err := tx.Load("holder")
		if err != nil {
			return err
		}
		holder.RoundCPUs = []int{1}
		holder.Builder.PID = 4001
		if err := tx.Save(holder); err != nil {
			return err
		}
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.RoundCPUs = []int{1, 2}
		b.RoundCPU = &one
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !reflect.DeepEqual(got.RoundCPUs, []int{0, 2}) {
		t.Errorf("RoundCPUs = %v, want the re-picked set [0 2]", got.RoundCPUs)
	}
	if got.RoundCPU == nil || *got.RoundCPU != 0 {
		t.Errorf("RoundCPU = %s, want the re-picked set's first core 0", cpuPtrText(got.RoundCPU))
	}
}

// TestAssignRoundCPURePicksWhenStoredCoreLeftPool pins the relaunch-pool
// rule: a stored set with a core outside the pool is re-picked, not kept.
func TestAssignRoundCPURePicksWhenStoredCoreLeftPool(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := cpuTestSetDeps(st, "0-2", 2)

	two := 2
	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.RoundCPUs = []int{2, 3}
		b.RoundCPU = &two
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !reflect.DeepEqual(got.RoundCPUs, []int{0, 1}) {
		t.Errorf("RoundCPUs = %v, want the re-picked set [0 1]", got.RoundCPUs)
	}
	if got.RoundCPU == nil || *got.RoundCPU != 0 {
		t.Errorf("RoundCPU = %s, want the re-picked set's first core 0", cpuPtrText(got.RoundCPU))
	}
}

// TestAssignRoundCPULegacySingleKeepsAsSet pins the legacy relaunch rule: a
// stored single pin with no list keeps as a size-1 set.
func TestAssignRoundCPULegacySingleKeepsAsSet(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := cpuTestDeps(st, "0-3")

	one := 1
	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.RoundCPU = &one
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !reflect.DeepEqual(got.RoundCPUs, []int{1}) {
		t.Errorf("RoundCPUs = %v, want the kept single as a set [1]", got.RoundCPUs)
	}
	if got.RoundCPU == nil || *got.RoundCPU != 1 {
		t.Errorf("RoundCPU = %s, want the kept core 1", cpuPtrText(got.RoundCPU))
	}
}

// TestAssignRoundCPUNoPoolGivesDistinctPairs pins the no-pool rule: with no
// template pool on a 4-CPU machine, two live rounds take distinct pairs and
// stay without a single pin, so gates and consults read unpinned.
func TestAssignRoundCPUNoPoolGivesDistinctPairs(t *testing.T) {
	fakeOnlineCPUs(t, 4)

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	saveCPUBinding(t, st, "second")
	deps := Deps{Topology: noTopology, Store: st, RoundCPUs: 2}

	var first, second store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		first = AssignRoundCPU(deps, tx, b)
		first.Builder.PID = 4001
		if err := tx.Save(first); err != nil {
			return err
		}
		b2, err := tx.Load("second")
		if err != nil {
			return err
		}
		second = AssignRoundCPU(deps, tx, b2)
		return tx.Save(second)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if !reflect.DeepEqual(first.RoundCPUs, []int{0, 1}) {
		t.Errorf("first RoundCPUs = %v, want [0 1]", first.RoundCPUs)
	}
	if first.RoundCPU != nil {
		t.Errorf("first RoundCPU = %s, want none without a pool", cpuPtrText(first.RoundCPU))
	}
	if !reflect.DeepEqual(second.RoundCPUs, []int{2, 3}) {
		t.Errorf("second RoundCPUs = %v, want [2 3]", second.RoundCPUs)
	}
	if second.RoundCPU != nil {
		t.Errorf("second RoundCPU = %s, want none without a pool", cpuPtrText(second.RoundCPU))
	}
}

// TestAssignRoundCPUNoPoolKeepsSetOnRelaunch pins the no-pool relaunch rule:
// a stored set still free on the same machine survives, still unpinned.
func TestAssignRoundCPUNoPoolKeepsSetOnRelaunch(t *testing.T) {
	fakeOnlineCPUs(t, 4)

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := Deps{Topology: noTopology, Store: st, RoundCPUs: 2}

	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.RoundCPUs = []int{2, 3}
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !reflect.DeepEqual(got.RoundCPUs, []int{2, 3}) {
		t.Errorf("RoundCPUs = %v, want the kept set [2 3]", got.RoundCPUs)
	}
	if got.RoundCPU != nil {
		t.Errorf("RoundCPU = %s, want none without a pool", cpuPtrText(got.RoundCPU))
	}
}

// noTopology stands in for the host's sysfs: every CPU alone at unknown
// speed, so a test's expected sets never depend on the machine running it.
func noTopology([]int) spawn.CPUTopology { return spawn.CPUTopology{} }

// TestAssignRoundCPUNilTopologyReadsHost pins the production default: with no
// Topology seam the host is read, and whatever it reports the round still
// gets a full set from the pool.
func TestAssignRoundCPUNilTopologyReadsHost(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	saveCPUBinding(t, st, "webshop")
	deps := Deps{Store: st, AllowedCPUs: "0-1", RoundCPUs: 2}

	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		got = AssignRoundCPU(deps, tx, b)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !reflect.DeepEqual(got.RoundCPUs, []int{0, 1}) {
		t.Errorf("RoundCPUs = %v, want [0 1]", got.RoundCPUs)
	}
}
