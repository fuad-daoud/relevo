package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// A fork child has nobody to tell: its end is the parent's child_ended, not a
// delivery to the MasterMind. The parent owns the whole fork, so it is the row
// that counts the children and decides whether the fork merges, joins or halts.

// chainForkChildEnded hands one child's end to its parent. It runs in the same
// transaction as the child's own save, so the parent's record of which children
// have ended can never disagree with the children's rows. The event names the
// fork the parent is standing at and the key the engine gave the child, so the
// parent's state can match the close against the child it is still waiting for.
//
// A parent row that is gone -- it was deleted, or it rolled the children back --
// is a warning, not a failure: the child still ends. A parent that has already
// reached a terminal status, is already merging, or has already recorded this
// child ignores the event, which is the engine's own replay guard.
func chainForkChildEnded(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, next workflow.State) error {
	parent, err := tx.Chain(c.Parent)
	if errors.Is(err, store.ErrNotFound) {
		slog.Warn("fork child ended with no parent row", "child", c.Name, "parent", c.Parent,
			"status", string(next.Status))
		return nil
	}
	if err != nil {
		return err
	}
	def, err := chainWorkflowDef(parent)
	if err != nil {
		return err
	}
	before, err := chainWorkflowState(parent)
	if err != nil {
		return err
	}
	ev := workflow.Event{
		Kind: workflow.EventChildEnded,
		// The parent is standing on the fork step, so its own At is the step
		// this close belongs to. The row's legacy step column cannot name it:
		// a fork is an unknown step to that projection and reads "building".
		Step:   before.At,
		Child:  chainForkChildKey(c),
		Status: string(next.Status),
		Reason: next.Reason,
	}
	after, acts := workflow.Next(def, cloneFlowState(before), ev)
	if len(acts) > 0 {
		return chainAdvance(ctx, rt, tx, parent, ev)
	}
	if len(after.Awaiting.Ended) == len(before.Awaiting.Ended) {
		return nil
	}
	// The fork is still waiting for its other children, so the transition has
	// no action to perform and the engine writes nothing. The parent's record
	// of this child is saved here instead: each child's end is its own
	// transaction, so without it a parent that reads its state back later would
	// wait on a child that has already ended, forever.
	return chainSaveForkAwait(tx, parent, def, after)
}

// chainSaveForkAwait records one child's end on the parent with no action
// behind it. chainSaveFlow's trace row names an action a caller performed;
// this is state alone, so the row is written on its own.
func chainSaveForkAwait(tx *store.Tx, c db.ChainRow, def workflow.Definition, next workflow.State) error {
	stateJSON, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("chain %s state: %w", c.Name, err)
	}
	row := c
	row.Status = string(next.Status)
	row.Reason = next.Reason
	row.StateJSON = stateJSON
	applyChainLegacy(&row, def, next)
	return tx.ChainPut(row)
}

// chainForkChildKey is the name a fork's child is known by inside its parent:
// the engine's own key, the digits it named the child "1".."N" in, which is the
// segment of the child's chain name after "<parent>.".
func chainForkChildKey(c db.ChainRow) string {
	if c.Parent == "" {
		return ""
	}
	return strings.TrimPrefix(c.Name, c.Parent+".")
}

// chainForkChildName is the chain name one fork child runs under.
func chainForkChildName(parent, key string) string {
	return fmt.Sprintf("%s.%s", parent, key)
}

// chainForkChildRef is the branch one fork child's work lands on, and the branch
// the parent merges in.
func chainForkChildRef(parent, key string) string {
	return "relevo/" + chainForkChildName(parent, key)
}
