package relevo

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// housekeepTeardown adapts the engine's worktree teardown to the plain values
// housekeep.GC reports.
func housekeepTeardown(rt Runtime) func(context.Context, store.Binding, bool) housekeep.TeardownOutcome {
	return func(ctx context.Context, b store.Binding, dryRun bool) housekeep.TeardownOutcome {
		o := worktreeTeardown(ctx, rt, b, dryRun)
		return housekeep.TeardownOutcome{Removed: o.Removed, Kept: o.Kept, Reason: o.Reason, Gone: o.Gone}
	}
}

// housekeepAskUnbind adapts the engine's server ask to housekeep.GC's ask.
func housekeepAskUnbind(rt Runtime) func(context.Context, store.Binding, bool) error {
	return func(ctx context.Context, b store.Binding, force bool) error {
		return askServerUnbind(ctx, rt, b, force)
	}
}

// HousekeepDeps builds internal/housekeep's Deps from rt. Git and Refs carry
// through nil-safe: a nil rt.Git converts to a nil interface on both, since
// the assignments are between identical interface shapes.
func HousekeepDeps(rt Runtime) housekeep.Deps {
	return housekeep.Deps{Store: rt.Store, TmpRoot: rt.TmpRoot, Git: rt.Git, Refs: rt.Git,
		ChainCheckSettled: chainCheckSettled, ChainFlowTerminal: chainFlowTerminal,
		GateUnit:        func(o, n string, r int) string { return scopeUnitNameFor(scopeGate, o, n, r, "") },
		ServedCheckUnit: func(b store.Binding, run store.CheckRun) string { return servedCheckSpec(rt, b, run).UnitName },
		ChainCheckUnit:  func(c db.ChainRow, run int) string { return chainCheckSpec(rt, c, run, "", "").UnitName },
		Teardown:        housekeepTeardown(rt),
		AskUnbind:       housekeepAskUnbind(rt),
		ErrKept:         ErrWorktreeKept,
		KeptReason:      KeptReasonOf,
		Abandon:         abandonSession,
		ClearProcess:    clearProcess,
		Halt:            housekeepHalt(rt),
		Preflight:       housekeepPreflight(rt),
		ShowCommand:     text.ShowCommand,
		Admit:           housekeepAdmit(rt),
		NotQueued:       ErrNotQueued,
		AllowedCPUs:     housekeepAllowedCPUs(rt),
		HeldCPUs:        rt.HeldCPUs,
		Scope:           rt.Scope,
		UnitName:        scopeUnitName,
		ScopeProber:     housekeepScopeProber(rt),
		Now:             housekeepNow(rt),
		HaltChainEnv:    housekeepHaltChainEnv(rt)}
}

// housekeepHalt adapts the engine's halt-and-settle to housekeep's halt.
func housekeepHalt(rt Runtime) func(context.Context, *store.Tx, store.Binding, string) (store.Binding, error) {
	return func(ctx context.Context, tx *store.Tx, b store.Binding, message string) (store.Binding, error) {
		return haltAndSettle(ctx, rt, tx, b, message)
	}
}

// housekeepPreflight adapts the engine's disk preflight to housekeep's probe.
func housekeepPreflight(rt Runtime) func(string) error {
	return func(tree string) error { return preflightDisk(rt, tree) }
}

// housekeepAdmit adapts the engine's queued-round admission to housekeep's admit.
func housekeepAdmit(rt Runtime) func(context.Context, string) error {
	return func(ctx context.Context, name string) error { return Admit(ctx, rt, name) }
}

// housekeepAllowedCPUs reads the runtime scope's allowed_cpus: empty with
// scopes off, so the round runs unpinned.
func housekeepAllowedCPUs(rt Runtime) string {
	if rt.Scope == nil {
		return ""
	}
	return rt.Scope.AllowedCPUs
}

// housekeepScopeProber is the runner's journal probe when it has one: a runner
// without it never reports an oom kill.
func housekeepScopeProber(rt Runtime) spawn.ScopeResultProber {
	p, _ := rt.Runner.(spawn.ScopeResultProber)
	return p
}

// housekeepNow is the runtime's clock, or the wall clock on a runtime with none.
func housekeepNow(rt Runtime) func() time.Time {
	if rt.Now != nil {
		return rt.Now
	}
	return time.Now
}

// housekeepHaltChainEnv runs the engine's chain halt for an environment note:
// the definition and state reads, the cloned halted state and the terminal
// transition, for the check row the caller already loaded.
func housekeepHaltChainEnv(rt Runtime) func(context.Context, *store.Tx, db.ChainRow, db.ChainCheckRow, workflow.Event) (bool, error) {
	return func(ctx context.Context, tx *store.Tx, c db.ChainRow, row db.ChainCheckRow, ev workflow.Event) (bool, error) {
		def, err := chain.WorkflowDef(c)
		if err != nil {
			return false, err
		}
		before, err := chain.WorkflowState(c)
		if err != nil {
			return false, err
		}
		next := cloneFlowState(before)
		next.Status = workflow.StatusHalted
		next.Reason = fmt.Sprintf("check %s: %s", row.Step, row.Note)
		return true, chainTerminalWF(ctx, rt, tx, c, def, before, next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
	}
}
