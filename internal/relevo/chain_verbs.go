package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainStopNoRoundReason is the trace row's reason when no member round was
// there to raise the event: the chain still ends, and the row says why it
// ended without a close. The event reasons are the chain's own vocabulary
// for "a human acted on this chain".
const (
	chainStopNoRoundReason = "no open member round"
	// ChainStopActionStopped is the StopResult.Action a chain stop reports
	// when its member had no open round and the chain was stopped directly:
	// nothing on the member was stopped and the chain ended.
	ChainStopActionStopped = "stopped"
	chainDoneReason        = "done"
)

// ErrChainRunning reports a verb refused because a chain still owns its
// members: `relevo done` on a running chain. The CLI maps it to a conflict,
// so a script can tell "stop it first" from an internal failure.
var ErrChainRunning = errors.New("the chain is still running")

// ErrChainDone reports a verb refused because a chain has finished: nothing
// continues a done chain. The CLI maps it to a conflict, the same class its
// running sibling has.
var ErrChainDone = errors.New("the chain is done")

// ChainStop ends a running chain: `relevo stop <n>` where <n> names a chain.
// The member the chain is waiting on is stopped exactly as `relevo stop` stops
// any binding, and that member's stopped close raises the chain's `stopped`
// event, which marks the chain stopped and queues its one end delivery.
//
// A chain that is not running is stopped through its member, whose open round
// is ended the way any binding's round is: the member may carry the chain's
// own name, so this verb is the only route to it. A chain with no open member
// round has nothing to stop: ErrNothingToStop, the answer `relevo stop`
// already gives and the CLI reports as exit 0. When a running chain's awaited
// member has no open round, nothing would ever raise the event, so the chain
// is stopped directly under the state lock instead.
//
// A chain waiting on fork children takes its children down with it, so no
// child is left running under a stopped parent.
func ChainStop(ctx context.Context, rt Runtime, name string) (StopResult, error) {
	c, err := rt.Store.Chain(name)
	if errors.Is(err, store.ErrNotFound) {
		return StopResult{}, fmt.Errorf("chain %s: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return StopResult{}, err
	}
	if chain.OnServer(c) {
		return chainServerStop(ctx, rt, c)
	}
	if c.Status != string(chain.StatusRunning) {
		return chainStopHaltedMember(ctx, rt, c)
	}
	res, err := chainStopWorkflow(ctx, rt, c)
	if err != nil {
		return StopResult{}, err
	}
	if err := chainStopForkChildren(ctx, rt, c); err != nil {
		return StopResult{}, err
	}
	return res, nil
}

// chainStopHaltedMember stops the awaited member's round on a chain that has
// already left running. The member may carry the chain's own name, so
// `relevo stop <member>` resolves to this very verb and the round is reachable
// only from here; ending it is what keeps a halted chain with an open manual
// round from having no working command at all, since the resume refuses the
// open round.
//
// A halted chain with no open member round answers with its own refusal,
// naming `relevo done` as the command that closes it. A stopped chain keeps
// the nothing-to-stop answer, so the refusal is asked of the halted status
// alone.
func chainStopHaltedMember(ctx context.Context, rt Runtime, c db.ChainRow) (StopResult, error) {
	member, _, ok := chain.Awaited(rt.Store, c)
	if !ok {
		return StopResult{}, chainStoppedNothing(c)
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		return StopResult{}, chainStoppedNothing(c)
	}
	entries, err := rt.Store.ReadLog(member)
	if err != nil || !roundOpenIn(entries, b.Round) {
		return StopResult{}, chainStoppedNothing(c)
	}
	return Stop(ctx, rt, member, StopOptions{})
}

// chainStoppedNothing is the answer a chain that has already left running gives
// when it has no open member round: the halt names the command that closes it,
// and every other ended chain keeps ErrNothingToStop.
func chainStoppedNothing(c db.ChainRow) error {
	if chain.Status(c.Status) == chain.StatusHalted {
		return chain.HaltedStop(c.Name)
	}
	return ErrNothingToStop
}

// chainStopWorkflow stops a workflow chain's awaited member, resolved through
// chain_member; stopping that member's round raises the chain's stopped event.
// An awaited check, a removed or missing member, and an already-closed round
// each stop directly under the lock instead.
func chainStopWorkflow(ctx context.Context, rt Runtime, c db.ChainRow) (StopResult, error) {
	st, err := chain.WorkflowState(c)
	if err != nil {
		return StopResult{}, err
	}
	if st.Awaiting.Member == "" {
		if derr := chainStopWorkflowDirect(ctx, rt, c.Name); derr != nil {
			return StopResult{}, derr
		}
		return StopResult{Round: st.Awaiting.Round, Action: ChainStopActionStopped}, nil
	}
	memberName, err := chainStoredMemberName(rt, c, st.Awaiting.Member)
	if err != nil {
		return StopResult{}, err
	}
	if memberName == "" {
		if derr := chainStopWorkflowDirect(ctx, rt, c.Name); derr != nil {
			return StopResult{}, derr
		}
		return StopResult{Round: st.Awaiting.Round, Action: ChainStopActionStopped}, nil
	}
	res, err := Stop(ctx, rt, memberName, StopOptions{})
	if errors.Is(err, ErrNothingToStop) || errors.Is(err, store.ErrNotFound) {
		if derr := chainStopWorkflowDirect(ctx, rt, c.Name); derr != nil {
			return StopResult{}, derr
		}
		return StopResult{Round: st.Awaiting.Round, Action: ChainStopActionStopped}, nil
	}
	if err != nil {
		return StopResult{}, err
	}
	return res, nil
}

// chainStoredMemberName resolves the binding that runs actor on a stored chain,
// so a stop names the member the engine's awaited step runs on.
func chainStoredMemberName(rt Runtime, c db.ChainRow, actor string) (string, error) {
	rows, err := rt.Store.ChainMembers(c.Name)
	if err != nil {
		return "", err
	}
	for _, m := range rows {
		if chain.FlowActorMatch(m.Actor, actor) {
			return m.Binding, nil
		}
	}
	return "", nil
}

// chainStopWorkflowDirect marks a running workflow chain stopped when the step
// it awaits has no open member round to end: the engine's stopped state, one
// trace row and the one end delivery, written under the state lock in the
// shape the engine's own stop uses. A check still in flight is killed and
// recorded as stopped first.
func chainStopWorkflowDirect(ctx context.Context, rt Runtime, name string) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		if c.Status != string(chain.StatusRunning) {
			return nil
		}
		if err := chainKillRunningChecks(ctx, rt, tx, c); err != nil {
			return err
		}
		def, err := chain.WorkflowDef(c)
		if err != nil {
			return err
		}
		before, err := chain.WorkflowState(c)
		if err != nil {
			return err
		}
		next := before
		next.Status = workflow.StatusStopped
		ev := workflow.Event{
			Kind: workflow.EventStopped, Step: before.Awaiting.Step,
			Member: before.Awaiting.Member, Round: before.Awaiting.Round, Run: before.Awaiting.Run,
			Reason: chainStopNoRoundReason,
		}
		act := workflow.Action{Kind: workflow.ActionStop, Step: before.Awaiting.Step, Reason: chainStopNoRoundReason}
		return chainTerminalWF(ctx, rt, tx, c, def, before, next, ev, act)
	})
}

// ChainDone releases a chain the human is finished with: `relevo done <n>`
// where <n> names a chain. Every member that exists is released through the
// ordinary `Done`, builder first; the chain is then marked done with one
// trace row and no MasterMind delivery, because the human ran this verb.
//
// A running chain is refused: `relevo stop <n> first`. A member's failure
// returns its error and leaves the chain's status unchanged, so the verb can
// be run again once the member is fixed.
func ChainDone(ctx context.Context, rt Runtime, name string) (DoneResult, error) {
	c, err := rt.Store.Chain(name)
	if errors.Is(err, store.ErrNotFound) {
		return DoneResult{}, fmt.Errorf("chain %s: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return DoneResult{}, err
	}
	if chain.OnServer(c) {
		return chainServerDone(ctx, rt, c)
	}
	if c.Status == string(chain.StatusRunning) {
		return DoneResult{}, fmt.Errorf("chain %s is running; relevo stop %s first: %w", c.Name, c.Name, ErrChainRunning)
	}

	// A row that predates the workflow engine is migrated onto it here, and a
	// row that still carries neither a workflow nor a state is refused in the
	// input class instead; both land before anything is released, so the verb
	// can be run again.
	c, err = chainDoneWorkflowRow(rt, c)
	if err != nil {
		return DoneResult{}, err
	}

	// A check left running by an earlier stop still owns the chain's tree:
	// kill and record it stopped before any member's worktree is released.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain(name)
		if err != nil {
			return err
		}
		return chainKillRunningChecks(ctx, rt, tx, row)
	}); err != nil {
		return DoneResult{}, err
	}

	// The builder's result is the chain's: its worktree and branch are the
	// chain's tree.
	var out DoneResult
	for i, member := range view.EndMembers(rt.Store, c) {
		res, derr := Done(ctx, rt, member)
		if errors.Is(derr, store.ErrNotFound) {
			// A member whose record is gone is already released.
			continue
		}
		if derr != nil {
			return DoneResult{}, derr
		}
		if i == 0 {
			out = res
		}
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain(name)
		if err != nil {
			return err
		}
		if row.Status == string(chain.StatusDone) {
			return nil
		}
		return chainDoneRow(rt, tx, row)
	}); err != nil {
		return DoneResult{}, err
	}
	// The chain is released: its end is done, so the seed copies it kept are
	// no longer needed.
	chainInputsSweep(rt, name, string(chain.StatusDone))
	return out, nil
}

// chainDoneWorkflowRow is the done path's read of the chain row's engine
// definition and state. The sweep that migrates pre-engine rows runs at
// daemon start, so a verb can meet a row the sweep could not finish. A row
// with no workflow is migrated here, through the same single-row conversion
// the sweep runs. Whatever still lacks a workflow or a state is refused in
// the input class instead, naming the row's own reason and the command that
// reads it; the wording is this verb's own -- the resume path's refusals say
// a chain cannot be resumed, which is the wrong verb here.
func chainDoneWorkflowRow(rt Runtime, c db.ChainRow) (db.ChainRow, error) {
	if len(c.WorkflowJSON) == 0 {
		fresh, err := convertLegacyChainRow(rt, c.Name)
		if err != nil {
			return c, err
		}
		c = fresh
	}
	_, derr := chain.WorkflowDef(c)
	_, serr := chain.WorkflowState(c)
	why := derr
	if why == nil {
		why = serr
	}
	if why == nil {
		return c, nil
	}
	if c.Reason != "" {
		return c, refusal.New(
			"chain %s cannot be done: %v; its own reason reads %q -- `relevo status %s` shows it",
			c.Name, why, c.Reason, c.Name)
	}
	return c, refusal.New(
		"chain %s cannot be done: %v; this row predates the workflow engine and carries nothing to migrate -- `relevo status %s` shows what it holds",
		c.Name, why, c.Name)
}

// chainInputsSweep removes a chain's inputs directory when the chain has ended
// done or stopped. A halted chain keeps it, since it may still be resumed,
// and the chain's own plan-i.md copies beside it are never touched. A removal
// failure is logged, never returned: the transition has already been written,
// and failing it would undo a fact.
func chainInputsSweep(rt Runtime, name, status string) {
	if status != string(chain.StatusDone) && status != string(chain.StatusStopped) {
		return
	}
	if err := os.RemoveAll(rt.Store.ChainInputDir(name)); err != nil {
		slog.Warn("chain inputs sweep", "chain", name, "status", status, "err", err)
	}
}

// chainDoneRow is the chain's own close: the engine state's status done, its
// legacy columns projected, and one workflow trace row. No payload is queued,
// since the human ran this verb.
func chainDoneRow(rt Runtime, tx *store.Tx, c db.ChainRow) error {
	def, err := chain.WorkflowDef(c)
	if err != nil {
		return err
	}
	before, err := chain.WorkflowState(c)
	if err != nil {
		return err
	}
	next := before
	next.Status = workflow.StatusDone

	member, carrier, ok, err := chainDeliveryMember(tx, c)
	if err != nil {
		return err
	}
	part := ""
	round := 0
	if ok {
		part = chain.PartOf(c, member)
		round = carrier.Round
	}
	ev := workflow.Event{Kind: workflow.EventNeedsYou, Member: part, Round: round, Reason: chainDoneReason}
	act := workflow.Action{Kind: workflow.ActionFinish}
	return chainSaveFlow(rt, tx, c, def, before, next, ev, act, member)
}

// supersedeChainDelivery confirms a chain's undelivered end payload: a halt's
// delivery queued before a resume says the chain needs the human, and that is
// no longer true the moment they resume it.
func supersedeChainDelivery(rt Runtime, c db.ChainRow) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		for _, member := range view.EndMembers(tx, c) {
			pending, err := tx.PendingForMasterMindThrough(member, 0)
			if err != nil {
				return err
			}
			for _, p := range pending {
				if p.Entry.Kind != store.KindChain {
					continue
				}
				if err := tx.ConfirmIndex(member, p.Idx, "superseded by resume"); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
