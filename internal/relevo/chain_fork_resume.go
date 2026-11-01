package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// Resuming one half of a fork: a child brings its parent back into the halted
// fork; a parent enters the earned join, never a second fork. There is no
// third outcome, because a fork's children are created once, at the fork.

// chainForkParentGate refuses a child's resume when the parent cannot follow
// it back into the fork. It is asked before the resume writes anything, so a
// refusal leaves the child exactly as the human found it.
//
// A running parent needs no re-opening: the child's own end reaches it as
// that child_ended. A parent halted for another reason, stopped, or done
// cannot re-open, and resuming the child would leave a running child nobody
// waits for. A gone parent row is left to the child's own failure, which
// already warns about a missing parent and ends the child quietly.
func chainForkParentGate(rt Runtime, c db.ChainRow) error {
	if c.Parent == "" {
		return nil
	}
	var parent db.ChainRow
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		got, err := tx.Chain(c.Parent)
		if err != nil {
			return err
		}
		parent = got
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return chainForkParentRefusal(parent, c)
}

// chainForkParentRefusal names the parent's status and the command that does
// work, so a human is never left with a message they cannot act on.
func chainForkParentRefusal(parent, child db.ChainRow) error {
	key := chainForkChildKey(child)
	switch chain.Status(parent.Status) {
	case chain.StatusRunning:
		return nil
	case chain.StatusStopped:
		return refusal.New("chain %s cannot resume: its parent %s is stopped; `relevo chain --resume --name %s` continues it",
			child.Name, parent.Name, parent.Name)
	case chain.StatusDone:
		return refusal.New("chain %s cannot resume: its parent %s is done", child.Name, parent.Name)
	}
	st, err := chain.WorkflowState(parent)
	if err != nil {
		return err
	}
	if _, ok := workflow.ReopenFork(st, key); ok {
		return nil
	}
	return refusal.New("chain %s cannot resume: its parent %s is halted at step %q for a reason this child does not answer; `relevo chain --resume --name %s` continues it",
		child.Name, parent.Name, flowTerminalStep(st), parent.Name)
}

// chainForkParentReopen re-opens a parent that halted over a child that just
// resumed, in the resume's own transaction, so the parent's record of ended
// children never disagrees with the running child.
//
// A parent that does not re-open is left exactly as it is: the engine's own
// guard already ignores a late close, and a running parent needs no write.
func chainForkParentReopen(rt Runtime, tx *store.Tx, child db.ChainRow) error {
	if child.Parent == "" {
		return nil
	}
	parent, err := tx.Chain(child.Parent)
	if errors.Is(err, store.ErrNotFound) {
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
	key := chainForkChildKey(child)
	next, ok := workflow.ReopenFork(before, key)
	if !ok {
		return nil
	}
	return chainSaveForkReopen(rt, tx, parent, def, before, next, key)
}

// chainSaveForkReopen writes a re-opened parent: running again, awaiting the
// unheard children, with one trace row naming the resuming child. The row
// carries no event or action, because no close arrived.
func chainSaveForkReopen(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before, next workflow.State, key string) error {
	stateJSON, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("chain %s state: %w", c.Name, err)
	}
	row := c
	row.Status = string(next.Status)
	row.Reason = next.Reason
	row.StateJSON = stateJSON
	applyChainLegacy(&row, def, next)
	trace := db.ChainEventRow{
		TS: rt.Now().UTC(), Step: flowTraceStep(before, workflow.Event{}),
		Member: row.Builder, Plan: flowPlanPos(next), Reason: "reopened by child " + key,
	}
	return tx.ChainSaveWithEvent(row, trace)
}

// chainForkResumeRefusal refuses the resume of a chain standing on a fork
// while any child still stands. It is asked before anything is written, so
// the refusal leaves the chain's own pending end delivery alone too. A fork
// whose children all ended done has earned its join and is not refused here.
func chainForkResumeRefusal(c db.ChainRow, before workflow.State) error {
	if len(before.Awaiting.Children) == 0 {
		return nil
	}
	waiting := forkUndoneChildren(before)
	if len(waiting) == 0 {
		return nil
	}
	return chainForkWaitRefusal(c, before, waiting)
}

// chainForkResumeParent answers a fork parent's resume, reporting whether it
// did: handled is false for every other chain.
//
// The parent does not re-enter the fork step: entering a fork starts its
// children again, replacing the ones it already waits on. So the fork is
// answered where it stands instead, running the merge the earned join calls
// for.
func chainForkResumeParent(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State) (bool, ChainResult, error) {
	if len(before.Awaiting.Children) == 0 {
		return false, ChainResult{}, nil
	}
	next := cloneFlowState(before)
	next.Status = workflow.StatusRunning
	next.Reason = ""
	next.Awaiting.Merging = true
	act := workflow.Action{
		Kind: workflow.ActionMerge, Step: before.Awaiting.Step,
		Reason: "re-entered the join after every child ended",
	}
	ev := workflow.Event{Kind: workflow.EventNeedsYou, Step: before.Awaiting.Step}
	if err := chainFlowMerge(ctx, rt, tx, c, def, before, &next, ev, act); err != nil {
		return true, ChainResult{}, err
	}
	after, err := tx.Chain(c.Name)
	if err != nil {
		return true, ChainResult{}, err
	}
	members, err := chainFlowStoredMembers(tx, after)
	if err != nil {
		return true, ChainResult{}, err
	}
	return true, ChainResult{
		Chain: after, Members: members, Plans: len(before.Iter["plans"].Items),
	}, nil
}

// forkUndoneChildren are a fork's children that have not ended done, in
// engine order. A child the fork never heard from reads as running, which is
// what it is.
func forkUndoneChildren(before workflow.State) []string {
	var out []string
	for _, key := range before.Awaiting.Children {
		if before.Awaiting.Ended[key].Status != "done" {
			out = append(out, key)
		}
	}
	return out
}

// chainForkWaitRefusal refuses a parent resume while a child still stands,
// naming every unended child and the command continuing one of them. The
// human resumes the child, which re-opens the parent; resuming the parent
// would leave the fork waiting on a chain nobody moved.
func chainForkWaitRefusal(c db.ChainRow, before workflow.State, waiting []string) error {
	parts := make([]string, 0, len(waiting))
	for _, key := range waiting {
		status := before.Awaiting.Ended[key].Status
		if status == "" {
			status = "still running"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", chainForkChildName(c.Name, key), status))
	}
	return refusal.New("chain %s is halted at fork %q and cannot be resumed: %s; `relevo chain --resume --name %s` continues it",
		c.Name, before.Awaiting.Step, strings.Join(parts, ", "), chainForkChildName(c.Name, waiting[0]))
}
