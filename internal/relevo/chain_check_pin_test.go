package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedPinHolder plants a live binding holding cores 0 and 1, so a fresh
// assignment in these tests must avoid the member's last set.
func seedPinHolder(t *testing.T, rt Runtime) {
	t.Helper()
	if err := rt.Store.Save(store.Binding{
		Name: "holder", CWD: "/holder", Round: 1, RoundCPUs: []int{0, 1},
		Builder: store.Endpoint{PID: 4242},
	}); err != nil {
		t.Fatalf("save holder: %v", err)
	}
}

// pinScope is the fake runner's recorded scope for the check start: the one
// Start the helpers below make.
func pinScope(t *testing.T, fr *fakeRunner) string {
	t.Helper()
	if len(fr.specs) != 1 {
		t.Fatalf("starts = %d, want 1", len(fr.specs))
	}
	scope := fr.specs[0].Scope
	if scope == nil {
		t.Fatal("check spec.Scope = nil, want a scope from the template")
	}
	if scope.AllowedCPUs != scope.AffinityCPUs {
		t.Errorf("Scope = %+v, want AllowedCPUs and AffinityCPUs to carry the same set", scope)
	}
	return scope.AffinityCPUs
}

// TestChainCheckRunsOnTheMembersLastSet pins the retained pin: the member's
// round closes with its set, and the check after the close runs on it.
//
// Mutation check: restore the advance's clears and the pin reads a fresh set
// instead of 0,1.
func TestChainCheckRunsOnTheMembersLastSet(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{CPUWeight: 100, AllowedCPUs: "0-3", RoundCPUs: 2}
	seedPinHolder(t, rt)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(c.Builder)
		if err != nil {
			return err
		}
		b.RoundCPUs = []int{0, 1}
		advanced, err := advanceToNextRound(context.Background(), rt, tx, b, nil, pathscope.Verdict{}, &queueReportState{})
		if err != nil {
			return err
		}
		return tx.Save(advanced)
	}); err != nil {
		t.Fatalf("close the member: %v", err)
	}

	chainStartCheckForTest(t, rt, c, "check", "make check")
	if got := pinScope(t, fr); got != "0,1" {
		t.Errorf("check pin = %q, want the member's last set 0,1", got)
	}
}

// TestChainCheckAssignsAFreshSetWhenTheMemberKeptNone pins the fresh pin: a
// member with no set gets one through the census, saved on the member, and the
// check runs on it.
func TestChainCheckAssignsAFreshSetWhenTheMemberKeptNone(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{CPUWeight: 100, AllowedCPUs: "0-3", RoundCPUs: 2}
	seedPinHolder(t, rt)

	chainStartCheckForTest(t, rt, c, "check", "make check")
	if got := pinScope(t, fr); got != "2,3" {
		t.Errorf("check pin = %q, want the fresh set 2,3", got)
	}
	b, err := rt.Store.Load(c.Builder)
	if err != nil {
		t.Fatalf("Load member: %v", err)
	}
	if len(b.RoundCPUs) != 2 || b.RoundCPUs[0] != 2 || b.RoundCPUs[1] != 3 {
		t.Errorf("member RoundCPUs = %v, want the saved fresh set [2 3]", b.RoundCPUs)
	}
}

// TestChainCheckPinSkipsAMissingOrRemoteMember pins the pin's refusals: a
// member with no binding and a member whose tree is not on this host both
// pin nothing and assign nothing.
func TestChainCheckPinSkipsAMissingOrRemoteMember(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainCheckFixture(t)
	plant := func(c db.ChainRow) {
		t.Helper()
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			return tx.ChainPut(c)
		}); err != nil {
			t.Fatalf("plant chain: %v", err)
		}
	}
	plant(db.ChainRow{ID: db.NewID(), Name: "ghost-chain", Status: "running", Phase: "build", Step: "building",
		Plan: 1, Plans: 1, Builder: "ghost", CreatedAt: baseTime, UpdatedAt: baseTime})
	if err := rt.Store.Save(store.Binding{Name: "far", CWD: "/far", Builder: store.Endpoint{Mode: store.ModeRemote, Server: "srv"}}); err != nil {
		t.Fatalf("save remote member: %v", err)
	}
	plant(db.ChainRow{ID: db.NewID(), Name: "far-chain", Status: "running", Phase: "build", Step: "building",
		Plan: 1, Plans: 1, Builder: "far", CreatedAt: baseTime, UpdatedAt: baseTime})

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, name := range []string{"ghost-chain", "far-chain"} {
			c, err := tx.Chain(name)
			if err != nil {
				return err
			}
			if err := chainCheckEnsureSet(rt, tx, c); err != nil {
				t.Errorf("chainCheckEnsureSet(%s): %v, want nil", name, err)
			}
			if got := chainCheckPin(tx, c); got != "" {
				t.Errorf("chainCheckPin(%s) = %q, want no pin", name, got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if b, err := rt.Store.Load("far"); err != nil {
		t.Fatalf("Load far: %v", err)
	} else if len(b.RoundCPUs) != 0 {
		t.Errorf("remote member RoundCPUs = %v, want none assigned", b.RoundCPUs)
	}
}

// TestChainCheckHoldsItsCPUs pins the census end to end: a running chain
// check holds its member's set, so the next binding's assignment avoids it.
//
// Mutation check: count the map out of HeldInChecks, or stop passing it in
// the HousekeepDeps closure, and the pick lands back on 0,1.
func TestChainCheckHoldsItsCPUs(t *testing.T) {
	t.Parallel()

	rt, _, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{CPUWeight: 100, AllowedCPUs: "0-3", RoundCPUs: 2}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(c.Builder)
		if err != nil {
			return err
		}
		b.RoundCPUs = []int{0, 1}
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("pin the member: %v", err)
	}
	chainStartCheckForTest(t, rt, c, "check", "make check")

	deps := HousekeepDeps(rt)
	deps.Topology = func([]int) spawn.CPUTopology { return spawn.CPUTopology{} }
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		got := housekeep.AssignRoundCPU(deps, tx, store.Binding{Name: "other", CWD: "/other", Round: 1})
		if len(got.RoundCPUs) != 2 || got.RoundCPUs[0] != 2 || got.RoundCPUs[1] != 3 {
			t.Errorf("fresh RoundCPUs = %v, want [2 3] avoiding the check's 0,1", got.RoundCPUs)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}
