package housekeep

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestPickCPU pins the allocator: the lowest core in the pool that no other
// live round holds; not ok when every core is held or the pool is empty; held
// cores outside the pool and duplicates are ignored.
func TestPickCPU(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		pool []int
		held []int
		want int
		ok   bool
	}{
		{"empty pool", nil, nil, 0, false},
		{"nothing held", []int{0, 1, 2}, nil, 0, true},
		{"first held", []int{0, 1, 2}, []int{0}, 1, true},
		{"first and last held", []int{0, 1, 2}, []int{0, 2}, 1, true},
		{"all held", []int{0, 1, 2}, []int{0, 1, 2}, 0, false},
		{"held outside the pool", []int{0, 1, 2}, []int{5, 0}, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := pickCPU(c.pool, c.held)
			if ok != c.ok || got != c.want {
				t.Errorf("pickCPU(%v, %v) = %d, %v; want %d, %v", c.pool, c.held, got, ok, c.want, c.ok)
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
		{Name: "self", RoundCPU: &one, Builder: store.Endpoint{PID: 42}},  // excluded: self
		{Name: "live", RoundCPU: &zero, Builder: store.Endpoint{PID: 42}}, // included
		{Name: "gated", RoundCPU: &one, GateRun: &store.GateRun{PID: 7}},  // included: gate running
		{Name: "dead", RoundCPU: &one},                                    // excluded: PID 0, no gate
		{Name: "unpinned", Builder: store.Endpoint{PID: 42}},              // excluded: nil RoundCPU
		{Name: "gated-unpinned", GateRun: &store.GateRun{PID: 7}},         // excluded: nil RoundCPU
	}
	want := []int{0, 1}
	if got := HeldIn(bindings, "self"); !reflect.DeepEqual(got, want) {
		t.Errorf("HeldIn = %v, want %v", got, want)
	}
}

// TestLocalHeldCPUs reads a real temp store's bindings through the caller's
// transaction, the local census path round starts use.
func TestLocalHeldCPUs(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
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
	return Deps{Store: st, AllowedCPUs: pool}
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

	st := store.New(t.TempDir())
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

// TestAssignRoundCPUExhaustedPoolRunsWholePool pins the exhaustion rule: when
// every pool core is held, the round runs on the whole pool with no RoundCPU.
func TestAssignRoundCPUExhaustedPoolRunsWholePool(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
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

	if second.RoundCPU != nil {
		t.Errorf("an exhausted pool left RoundCPU = %s, want none", cpuPtrText(second.RoundCPU))
	}
}

// TestAssignRoundCPUKeepsCoreOnRelaunch pins the relaunch rule: a binding
// whose RoundCPU is still in the pool and still free keeps it.
func TestAssignRoundCPUKeepsCoreOnRelaunch(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
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

	st := store.New(t.TempDir())
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

	st := store.New(t.TempDir())
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
