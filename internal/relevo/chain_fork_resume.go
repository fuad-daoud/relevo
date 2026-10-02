package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// Resuming one half of a fork. A child that resumes brings its parent back into
// the fork it halted at; a parent that resumes enters the join its children have
// earned, never a second fork. Both are refusals or one of those two moves --
// there is no third outcome, because a fork's children are created once, at the
// fork, and a resume has no business creating any more.

// chainForkParentGate refuses a child's resume when the parent cannot follow it
// back into the fork. It is asked before the resume writes anything, so a
// refusal leaves the child exactly as the human found it.
//
// A parent that is still running is waiting on its other children and needs no
// re-opening: the child's own end reaches it as that child_ended. A parent
// halted for some other reason, stopped, or done cannot re-open at all -- its
// halt is not one this child would answer -- and resuming the child would leave
// a running child nobody is waiting for. A parent whose row is gone is left to
// the child's own failure: the handoff already warns about a missing parent and
// ends the child quietly, and a resume must not be the thing that hard-fails.
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

// chainForkParentRefusal is the one refusal a child's resume makes: the parent's
// status is named, and so is the command that does work, so a human is never
// left with a message they cannot act on.
func chainForkParentRefusal(parent, child db.ChainRow) error {
	key := chainForkChildKey(child)
	switch chain.Status(parent.Status) {
	case chain.StatusRunning:
		return nil
	case chain.StatusStopped:
		return refuse("chain %s cannot resume: its parent %s is stopped; `relevo chain --resume --name %s` continues it",
			child.Name, parent.Name, parent.Name)
	case chain.StatusDone:
		return refuse("chain %s cannot resume: its parent %s is done", child.Name, parent.Name)
	}
	st, err := chainWorkflowState(parent)
	if err != nil {
		return err
	}
	if _, ok := workflow.ReopenFork(st, key); ok {
		return nil
	}
	return refuse("chain %s cannot resume: its parent %s is halted at step %q for a reason this child does not answer; `relevo chain --resume --name %s` continues it",
		child.Name, parent.Name, flowTerminalStep(st), parent.Name)
}

// chainForkParentReopen re-opens a parent that halted because of a child that
// has just resumed. It runs in the resume's own transaction, so the parent's
// record of which children have ended can never disagree with the child that is
// running again.
//
// A parent that does not re-open -- because it is still running on its other
// children, or because the child's halt is not the parent's reason -- is left
// exactly as it is. The engine's own guard already ignores a late close, and a
// running parent needs no write.
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
	def, err := chainWorkflowDef(parent)
	if err != nil {
		return err
	}
	before, err := chainWorkflowState(parent)
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

// chainSaveForkReopen writes a re-opened parent: its state, running again and
// awaiting the children it has not heard from, and one trace row naming the
// child whose resume did it. The row carries no event or action, because no
// close arrived -- a human resuming a child is not the engine's own transition,
// and the reason is the whole record of it.
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

// chainForkResumeRefusal refuses the resume of a chain standing on a fork while
// any child is still standing. It is asked before the resume writes anything --
// so the refusal also leaves the chain's own pending end delivery alone, which a
// halt's delivery is until a human acts. It is nil for every other chain.
//
// A fork whose children have all ended done has earned its join and is not
// refused here: chainForkResumeParent re-enters it.
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

// chainForkResumeParent is a fork parent's resume. It answers a resume of a chain
// standing on a fork, and reports whether it did: handled is false for every other
// chain, which resumes as it always did.
//
// The parent does not re-enter the fork step. A resume re-enters the step the
// chain halted on, and entering a fork starts its children again -- replacing the
// children the parent is already waiting on with new ones, on the same keys. So
// the fork is answered where it stands instead: with a child still halted, the
// earlier refusal named the children to resume; with every child ended done, the
// fork has earned its join and the merge action runs.
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

// forkUndoneChildren are the children of a fork that have not ended done, in the
// order the engine named them. A child the fork has not heard from at all reads
// as running, which is what it is.
func forkUndoneChildren(before workflow.State) []string {
	var out []string
	for _, key := range before.Awaiting.Children {
		if before.Awaiting.Ended[key].Status != "done" {
			out = append(out, key)
		}
	}
	return out
}

// chainForkWaitRefusal refuses a parent resume while a child is still standing:
// it names every child that has not ended done and the command that continues
// one of them. The human resumes the child, which re-opens the parent; resuming
// the parent instead would leave the fork waiting on a chain nobody moved.
func chainForkWaitRefusal(c db.ChainRow, before workflow.State, waiting []string) error {
	parts := make([]string, 0, len(waiting))
	for _, key := range waiting {
		status := before.Awaiting.Ended[key].Status
		if status == "" {
			status = "still running"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", chainForkChildName(c.Name, key), status))
	}
	return refuse("chain %s is halted at fork %q and cannot be resumed: %s; `relevo chain --resume --name %s` continues it",
		c.Name, before.Awaiting.Step, strings.Join(parts, ", "), chainForkChildName(c.Name, waiting[0]))
}