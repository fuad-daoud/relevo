package relevo

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// doneChainInScope is the settle set's predicate: only a done chain has handed
// its members back, so a halted, stopped or running chain never contributes.
// The mastermind half is GC's own scope, so the sweep settles exactly the
// chains a later GC with the same opts would clear the members of.
func doneChainInScope(opts housekeep.GCOptions, c db.ChainRow) bool {
	if c.Status != string(chain.StatusDone) {
		return false
	}
	return opts.AllMasterMinds || c.MasterMindID == opts.MasterMindID
}

// doneChainMembers names the members the sweep settles: the end verbs' own
// walk over every in-scope done chain, so the sweep releases exactly what
// `relevo done <chain>` would release for each of them.
func doneChainMembers(opts housekeep.GCOptions, chains []db.ChainRow, lister chain.MemberLister) []string {
	var out []string
	for _, c := range chains {
		if !doneChainInScope(opts, c) {
			continue
		}
		out = append(out, view.EndMembers(lister, c)...)
	}
	return out
}

// GCDoneChains settles every finished chain in scope, then clears what is
// done. A done chain's members are still live bindings, which GC never
// touches, so each chain is closed through its own verb first; the close
// marks its members done, which is exactly what the GC after it clears. The
// orchestration lives here and not in housekeep: housekeep must not import
// the engine's verbs or views.
//
// A dry run settles nothing: each member still owed a close is reported as
// would-clear beside whatever a dry-run GC already lists, and the store is
// untouched. A member with a kept worktree and no --force keeps its binding
// on the GC after the settle, the same as any DONE binding with a kept tree.
func GCDoneChains(ctx context.Context, rt Runtime, opts housekeep.GCOptions) ([]housekeep.GCResult, error) {
	switch {
	case opts.MasterMindID == "" && !opts.AllMasterMinds:
		return nil, housekeep.ErrGCNoScope
	case opts.MasterMindID != "" && opts.AllMasterMinds:
		return nil, fmt.Errorf("%w: MasterMindID and AllMasterMinds are exclusive", housekeep.ErrGCNoScope)
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return nil, err
	}
	var targets []db.ChainRow
	for _, c := range chains {
		if doneChainInScope(opts, c) {
			targets = append(targets, c)
		}
	}
	if opts.DryRun {
		return gcDoneChainsDryRun(ctx, rt, opts, targets)
	}
	for _, c := range targets {
		if _, err := ChainDone(ctx, rt, c.Name); err != nil {
			return nil, err
		}
	}
	return housekeep.GC(ctx, HousekeepDeps(rt), opts)
}

// gcDoneChainsDryRun reports the settle set without settling it: every
// selected member that is not done yet reads as would-clear, in the GCResult
// shape the dry-run rendering already prints, ahead of the dry-run GC's own
// rows. A member that is already done is left for the GC rows, so no name
// prints twice, and a member whose record is gone names nothing.
func gcDoneChainsDryRun(ctx context.Context, rt Runtime, opts housekeep.GCOptions, targets []db.ChainRow) ([]housekeep.GCResult, error) {
	seen := map[string]bool{}
	var out []housekeep.GCResult
	for _, name := range doneChainMembers(opts, targets, rt.Store) {
		if seen[name] {
			continue
		}
		seen[name] = true
		b, err := rt.Store.Load(name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.State == store.StateDone {
			continue
		}
		res := housekeep.GCResult{Name: b.Name, CWD: b.CWD, Rounds: b.Round, MasterMindID: b.MasterMindID}
		outcome := HousekeepDeps(rt).Teardown(ctx, b, true)
		res.WorktreeRemoved = outcome.Removed
		res.WorktreeKept = outcome.Kept
		res.KeptReason = outcome.Reason
		res.WorktreeGone = outcome.Gone
		res.BindingKept = outcome.Kept != "" && !opts.Force
		out = append(out, res)
	}
	done, err := housekeep.GC(ctx, HousekeepDeps(rt), opts)
	if err != nil {
		return nil, err
	}
	return append(out, done...), nil
}
