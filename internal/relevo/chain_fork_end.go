package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// A fork child's end is its parent's child_ended, not a MasterMind delivery:
// the parent owns the fork, so its row counts the children and decides merge,
// join or halt.

// chainForkChildEnded hands one child's end to its parent in the child's own
// transaction, so the parent's record of ended children never disagrees with
// the children's rows. The event names the fork step and the engine's child
// key, so the parent matches the close against the child it still awaits.
//
// A gone parent row is a warning, not a failure: the child still ends. A
// terminal, merging, or already-recorded parent ignores the event, the
// engine's own replay guard.
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
	def, err := chain.WorkflowDef(parent)
	if err != nil {
		return err
	}
	before, err := chain.WorkflowState(parent)
	if err != nil {
		return err
	}
	ev := workflow.Event{
		Kind: workflow.EventChildEnded,
		// The parent stands on the fork step, so its own At is the step this
		// close belongs to: the legacy step column cannot name a fork and
		// reads "building".
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
	// No action is due while the fork still waits on other children, so the
	// engine writes nothing and the parent's record of this child is saved
	// here instead: each end is its own transaction, and without it a parent
	// reading its state back later would wait on an ended child, forever.
	return chainSaveForkAwait(tx, parent, def, after)
}

// chainSaveForkAwait records one child's end on the parent. No action stands
// behind it, so unlike chainSaveFlow's trace row it is written on its own.
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

// chainForkChildKey is the name a fork's child is known by inside its
// parent: the engine's own "1".."N" key, the segment of the child's chain
// name after "<parent>.".
func chainForkChildKey(c db.ChainRow) string {
	if c.Parent == "" {
		return ""
	}
	return strings.TrimPrefix(c.Name, c.Parent+".")
}

func chainForkChildName(parent, key string) string {
	return fmt.Sprintf("%s.%s", parent, key)
}

// chainForkChildRef is the branch one fork child's work lands on, which the
// parent merges in.
func chainForkChildRef(parent, key string) string {
	return "relevo/" + chainForkChildName(parent, key)
}
