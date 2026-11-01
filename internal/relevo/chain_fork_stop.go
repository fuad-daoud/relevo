package relevo

import (
	"context"
	"errors"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Stopping a fork parent stops the whole tree under it. The parent goes first,
// so the engine's own replay guard discards the children's child_ended closes.
// Nothing merges: the parent no longer waits for the ends the merge needs.

// chainStopForkChildren stops every running child of a chain halted on a fork,
// after the parent is marked stopped, recursing through ChainStop so a child
// that is itself a fork takes its own children down in turn.
//
// The state is read once, before any child is touched: a stopped or done child
// is left alone, and a deleted row is skipped, already dealt with by a
// rollback or a manual deletion.
func chainStopForkChildren(ctx context.Context, rt Runtime, c db.ChainRow) error {
	st, err := chain.WorkflowState(c)
	if err != nil {
		return err
	}
	if len(st.Awaiting.Children) == 0 {
		return nil
	}
	for _, key := range st.Awaiting.Children {
		if err := chainStopForkChild(ctx, rt, c, key); err != nil {
			return err
		}
	}
	return nil
}

// chainStopForkChild stops one named child, reading its row fresh so a child a
// sibling's stop already reached is left alone.
func chainStopForkChild(ctx context.Context, rt Runtime, parent db.ChainRow, key string) error {
	name := chainForkChildName(parent.Name, key)
	child, err := rt.Store.Chain(name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if chain.Status(child.Status) != chain.StatusRunning {
		return nil
	}
	if _, err := ChainStop(ctx, rt, name); err != nil && !errors.Is(err, ErrNothingToStop) {
		return err
	}
	return nil
}
