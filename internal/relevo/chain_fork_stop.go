package relevo

import (
	"context"
	"errors"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Stopping a fork parent stops the whole tree under it. The parent goes first,
// so the engine's own replay guard -- a terminal run ignores any later event --
// is what discards the children's child_ended closes. Nothing merges: the parent
// is no longer waiting for the ends the merge would need.

// chainStopForkChildren stops every running child of a chain halted on a fork.
// It is called after the parent has been marked stopped, and it recurses through
// ChainStop, so a child that is itself a fork takes its own children down in
// turn.
//
// The state is read once, before any child is touched: a child that is already
// stopped or done is left alone, and one whose row has been deleted is skipped,
// because a rollback or a manual deletion has already dealt with it.
func chainStopForkChildren(ctx context.Context, rt Runtime, c db.ChainRow) error {
	st, err := chainWorkflowState(c)
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
// sibling's stop has already reached is left alone.
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

