package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// startCheckChain starts a check workflow and drives it to its check step, so
// one check run is in flight. It returns the run number and the run's row.
func startCheckChain(t *testing.T, rt Runtime) (int, db.ChainCheckRow) {
	t.Helper()
	startFlowChain(t, rt, flowCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	return flowCheckRun(t, rt, "shop")
}

// checkScopeUnit is the gate scope unit a chain check run is scoped to.
func checkScopeUnit(c db.ChainRow, run int) string {
	return scopeUnitNameFor(scopeGate, c.Owner, c.Name+"-check", run, "")
}

// forceChainStatus writes a chain's engine state and row status directly, the
// residue a stop from another path leaves, so a test can reach a verb on a
// chain whose check is still running.
func forceChainStatus(t *testing.T, rt Runtime, status workflow.Status) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		st, err := chainWorkflowState(c)
		if err != nil {
			return err
		}
		st.Status = status
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		c.StateJSON = raw
		c.Status = string(status)
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("force chain status %s: %v", status, err)
	}
}

// TestWorkflowStopKillsTheRunningCheck pins the stop's check arm: a chain that
// waits on a check is stopped by ending that check's scope and recording the run
// stopped, so no check outlives the chain that owns its tree.
func TestWorkflowStopKillsTheRunningCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := rt.Runner.(*fakeRunner)
	rt.Scope = &spawn.ScopeSpec{CPUQuota: "150%", GateCPUQuota: "300%", AllowedCPUs: "0-3"}

	run, _ := startCheckChain(t, rt)
	c := flowChainRow(t, rt)
	unit := checkScopeUnit(c, run)
	fr.scopeActive = map[string]bool{unit: true}

	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if !slices.Contains(fr.scopeStops, unit) {
		t.Errorf("scopeStops = %v, want the check's unit %s", fr.scopeStops, unit)
	}
	if got := loadChainCheck(t, rt, run); got.Result != chainCheckStopped {
		t.Errorf("check row result = %q, want %q", got.Result, chainCheckStopped)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(workflow.StatusStopped) {
		t.Errorf("chain status = %q, want stopped", row.Status)
	}
}

// TestWorkflowDoneKillsTheRunningCheck pins the done's check arm: a chain left
// stopped with its check still running is killed before any member's worktree is
// released, so a done never takes the tree out from under a live check.
func TestWorkflowDoneKillsTheRunningCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := rt.Runner.(*fakeRunner)
	rt.Scope = &spawn.ScopeSpec{CPUQuota: "150%", GateCPUQuota: "300%", AllowedCPUs: "0-3"}

	run, _ := startCheckChain(t, rt)
	c := flowChainRow(t, rt)
	unit := checkScopeUnit(c, run)
	fr.scopeActive = map[string]bool{unit: true}
	// The residue an earlier stop left: the chain reads stopped while its check
	// still runs. Done must kill it before releasing anything.
	forceChainStatus(t, rt, workflow.StatusStopped)

	if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainDone: %v", err)
	}
	if !slices.Contains(fr.scopeStops, unit) {
		t.Errorf("scopeStops = %v, want the check's unit %s", fr.scopeStops, unit)
	}
	if got := loadChainCheck(t, rt, run); got.Result != chainCheckStopped {
		t.Errorf("check row result = %q, want %q", got.Result, chainCheckStopped)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(workflow.StatusDone) {
		t.Errorf("chain status = %q, want done", row.Status)
	}
}

// TestWorkflowResumeDoesNotRunTwoChecksAtOnce pins the resume guard: a resume
// that re-enters a check step whose earlier run is still alive adopts that run
// rather than starting a second one beside it on the same tree.
func TestWorkflowResumeDoesNotRunTwoChecksAtOnce(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	run, _ := startCheckChain(t, rt)
	// The stopped state, with the check's process still alive: a resume must not
	// start run two.
	forceChainStatus(t, rt, workflow.StatusStopped)

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if second, err := rt.Store.ChainCheck("shop", 2); err == nil {
		t.Fatalf("a second check run exists: %+v", second)
	}
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" || st.Awaiting.Run != run {
		t.Errorf("awaiting = %+v, want the check awaiting run %d", st.Awaiting, run)
	}
}

// TestChainStartCheckKillsTheProcessWhenItsRowCannotBeWritten pins the ordering
// the security review asked for: the tracking row is written before the spawn,
// and when the PID fill cannot be written the spawned process is killed rather
// than left as an orphan.
func TestChainStartCheckKillsTheProcessWhenItsRowCannotBeWritten(t *testing.T) {
	rt, fr, c := chainCheckFixture(t)

	old := chainCheckPut
	chainCheckPut = func(tx *store.Tx, name string, row db.ChainCheckRow) error {
		return errors.New("injected row write failure")
	}
	t.Cleanup(func() { chainCheckPut = old })

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, serr := chainStartCheck(context.Background(), rt, tx, c, "check", "make check")
		return serr
	})
	if err == nil {
		t.Fatal("chainStartCheck = nil, want the injected row-write failure")
	}
	if len(fr.handles) != 1 {
		t.Fatalf("starts = %d, want the one spawn", len(fr.handles))
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != fr.handles[0].PID {
		t.Errorf("kills = %+v, want the spawned process killed", fr.kills)
	}
}

// TestCheckTickSkipsTerminalChains pins the tick's guard: a terminal chain, and
// a chain whose engine is not waiting on a check, are skipped before any row
// lookup or liveness probe, so the every-chain walk costs nothing for them.
func TestCheckTickSkipsTerminalChains(t *testing.T) {
	t.Parallel()

	t.Run("terminal", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		fr := rt.Runner.(*fakeRunner)
		run, _ := startCheckChain(t, rt)
		forceChainStatus(t, rt, workflow.StatusStopped)

		probes := 0
		fr.onAlive = func() { probes++ }
		tickChainChecks(context.Background(), rt)

		if probes != 0 {
			t.Errorf("liveness probes = %d, want none for a terminal chain", probes)
		}
		if got := loadChainCheck(t, rt, run); got.Result != "" {
			t.Errorf("check row result = %q, want it untouched", got.Result)
		}
	})

	t.Run("not awaiting a check", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		fr := rt.Runner.(*fakeRunner)
		startFlowChain(t, rt, flowCheckWorkflow)
		// A stale run exists while the engine waits on the builder: the tick
		// must not look it up or probe it.
		c := flowChainRow(t, rt)
		chainStartCheckForTest(t, rt, c, "check", "true")

		probes := 0
		fr.onAlive = func() { probes++ }
		tickChainChecks(context.Background(), rt)

		if probes != 0 {
			t.Errorf("liveness probes = %d, want none while a member is awaited", probes)
		}
	})
}
