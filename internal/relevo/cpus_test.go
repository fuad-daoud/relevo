package relevo

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestPickCPU pins #314's allocator: the lowest core in the pool that no other
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

// TestHeldIn pins #314's census rule: self is excluded; a binding with a
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
// transaction, the local census path startRound uses.
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

// TestRoundCPUJSON pins #314's field shape: core 0 is a valid pin and survives
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

// TestCPUPinText pins the pin string scopeFor consumes: "" when no core is
// pinned, the decimal core otherwise -- core 0 included.
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
			if got := cpuPinText(c.b); got != c.want {
				t.Errorf("cpuPinText = %q, want %q", got, c.want)
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

// bindSecond adds a second headless binding ("second", its own worktree) to rt
// so a test can give two live rounds distinct cores.
func bindSecond(t *testing.T, rt Runtime) store.Binding {
	t.Helper()
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "second", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo2",
	}); err != nil {
		t.Fatalf("Bind second: %v", err)
	}
	b, err := rt.Store.Load("second")
	if err != nil {
		t.Fatalf("Load second: %v", err)
	}
	return b
}

// TestTwoRoundsGetDistinctCores pins #314's allocation: with pool "0-1", the
// first round takes the lowest free core and, while it is live, the second
// takes the next one.
func TestTwoRoundsGetDistinctCores(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b2 := bindSecond(t, rt)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "0-1"}

	var first, second store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		if first, err = startRound(context.Background(), rt, tx, b, "one", false); err != nil {
			return err
		}
		if err := tx.Save(first); err != nil {
			return err
		}
		second, err = startRound(context.Background(), rt, tx, b2, "two", false)
		if err != nil {
			return err
		}
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
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2", len(fr.specs))
	}
	if got := fr.specs[0].Scope.AllowedCPUs; got != "0" {
		t.Errorf("first spec AllowedCPUs = %q, want 0", got)
	}
	if got := fr.specs[1].Scope.AllowedCPUs; got != "1" {
		t.Errorf("second spec AllowedCPUs = %q, want 1", got)
	}
}

// TestExhaustedPoolRunsOnWholePool pins #314's exhaustion rule: when every pool
// core is held, the round runs on the whole pool with no RoundCPU.
func TestExhaustedPoolRunsOnWholePool(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b2 := bindSecond(t, rt)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "0"}

	var second store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		first, err := startRound(context.Background(), rt, tx, b, "one", false)
		if err != nil {
			return err
		}
		if err := tx.Save(first); err != nil {
			return err
		}
		second, err = startRound(context.Background(), rt, tx, b2, "two", false)
		return err
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	if second.RoundCPU != nil {
		t.Errorf("an exhausted pool left RoundCPU = %s, want none", cpuPtrText(second.RoundCPU))
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2", len(fr.specs))
	}
	if got := fr.specs[1].Scope.AllowedCPUs; got != "0" {
		t.Errorf("second spec AllowedCPUs = %q, want the pool 0", got)
	}
}

// TestRelaunchKeepsItsCore pins #314's relaunch/switch rule: a binding whose
// RoundCPU is still in the pool and still free keeps it.
func TestRelaunchKeepsItsCore(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "0-3"}
	one := 1
	b.RoundCPU = &one

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = startRound(context.Background(), rt, tx, b, "again", false)
		return err
	})
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.RoundCPU == nil || *got.RoundCPU != 1 {
		t.Errorf("RoundCPU = %s, want the kept core 1", cpuPtrText(got.RoundCPU))
	}
	if fr.specs[0].Scope.AllowedCPUs != "1" {
		t.Errorf("spec AllowedCPUs = %q, want 1", fr.specs[0].Scope.AllowedCPUs)
	}
}

// TestRoundCloseReleasesCore pins #314's release: queueReport's reset block
// clears RoundCPU, and the next round on another binding can take that core.
func TestRoundCloseReleasesCore(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "0-1"}

	var first store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		first, err = startRound(context.Background(), rt, tx, b, "one", false)
		if err != nil {
			return err
		}
		return tx.Save(first)
	})
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if first.RoundCPU == nil || *first.RoundCPU != 0 {
		t.Fatalf("RoundCPU = %s, want 0", cpuPtrText(first.RoundCPU))
	}

	if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte("report body"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		next, err := queueReport(context.Background(), rt, tx, cur, entries, rt.Store.ReportPath(b.Name, b.Round), "done", "test", nil, nil, nil, nil, "", false, scopeVerdict{})
		if err != nil {
			return err
		}
		if next.RoundCPU != nil {
			t.Errorf("RoundCPU after queueReport = %s, want none", cpuPtrText(next.RoundCPU))
		}
		return tx.Save(next)
	})
	if err != nil {
		t.Fatalf("queueReport: %v", err)
	}

	b2 := bindSecond(t, rt)
	var second store.Binding
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		second, err = startRound(context.Background(), rt, tx, b2, "two", false)
		if err != nil {
			return err
		}
		return tx.Save(second)
	})
	if err != nil {
		t.Fatalf("startRound second: %v", err)
	}
	if second.RoundCPU == nil || *second.RoundCPU != 0 {
		t.Errorf("second RoundCPU = %s, want the released core 0", cpuPtrText(second.RoundCPU))
	}
}

// TestNoPoolWritesNoField pins #314's off switch: a scope with no allowed_cpus
// leaves RoundCPU nil and the spec's AllowedCPUs empty.
func TestNoPoolWritesNoField(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = startRound(context.Background(), rt, tx, b, "x", false)
		return err
	})
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.RoundCPU != nil {
		t.Errorf("RoundCPU = %s, want none with no pool", cpuPtrText(got.RoundCPU))
	}
	if got := fr.specs[0].Scope.AllowedCPUs; got != "" {
		t.Errorf("spec AllowedCPUs = %q, want \"\"", got)
	}
}
