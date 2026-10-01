package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The two words a chain's own stop and done write. The reason is the trace
// row's when no member round was there to raise the event: the chain still ends,
// and the row says why it ended without a close. The event reasons are the
// chain's own vocabulary for "a human acted on this chain", whose text the
// trace renders -- round 1's event kinds carry no "stopped"/"done" verb of their
// own, exactly as the member sweep's row does not.
const (
	chainStopNoRoundReason = "no open member round"
	// ChainStopActionStopped is the StopResult.Action a chain stop reports when
	// its member had no open round and the chain was stopped directly. The
	// documented stop actions ("killed", "reaped", "gone", "dequeued",
	// "nothing") all describe a member's round, which is exactly what this one
	// does not: nothing on the member was stopped and the chain ended.
	ChainStopActionStopped = "stopped"
	chainDoneReason        = "done"
)

// ErrChainRunning reports a verb refused because a chain still owns its
// members: `relevo done` on a running chain. The CLI maps it to a conflict, so
// a script can tell "stop it first" from an internal failure.
var ErrChainRunning = errors.New("the chain is still running")

// ChainStop ends a running chain: `relevo stop <n>` where <n> names a chain.
// The member the chain is waiting on is stopped exactly as `relevo stop` stops
// any binding, and that member's stopped close raises the chain's `stopped`
// event, which marks the chain stopped and queues its one end delivery.
//
// A chain that is not running has nothing to stop: ErrNothingToStop, the answer
// `relevo stop` already gives and the CLI reports as exit 0. When the awaited
// member has no open round, nothing would ever raise the event, so the chain is
// stopped directly under the state lock instead.
func ChainStop(ctx context.Context, rt Runtime, name string) (StopResult, error) {
	c, err := rt.Store.Chain(name)
	if errors.Is(err, store.ErrNotFound) {
		return StopResult{}, fmt.Errorf("chain %s: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return StopResult{}, err
	}
	if c.Status != string(chain.StatusRunning) {
		return StopResult{}, ErrNothingToStop
	}

	memberName := chainMemberName(c, c.AwaitingMember)
	if memberName == "" {
		return StopResult{}, fmt.Errorf("chain %s has no %s member to stop", c.Name, c.AwaitingMember)
	}

	res, err := Stop(ctx, rt, memberName, StopOptions{})
	if errors.Is(err, ErrNothingToStop) {
		// The member's round already closed, so no close will raise the
		// chain's stopped event: the chain is stopped directly.
		if derr := chainStopDirect(ctx, rt, name); derr != nil {
			return StopResult{}, derr
		}
		return StopResult{Round: c.AwaitingRound, Action: ChainStopActionStopped}, nil
	}
	if err != nil {
		return StopResult{}, err
	}
	return res, nil
}

// chainStopDirect marks a running chain stopped when the member it awaits had
// no open round to end: the status, the one trace row and the one end delivery,
// written under the state lock in the shape the state machine's own stop uses.
// A chain that ended between the caller's read and this lock is left alone.
func chainStopDirect(ctx context.Context, rt Runtime, name string) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		if c.Status != string(chain.StatusRunning) {
			return nil
		}
		before, err := chainStateOf(c)
		if err != nil {
			return err
		}
		next := before
		next.Status = chain.StatusStopped
		ev := chain.Event{
			Kind: chain.EventStopped, Member: c.AwaitingMember,
			Round: before.Awaiting.Round, Reason: chainStopNoRoundReason,
		}
		act := chain.Action{Kind: chain.ActionStop, Reason: chainStopNoRoundReason}
		return chainTerminal(ctx, rt, tx, c, before, next, ev, act, chainMemberName(c, c.AwaitingMember))
	})
}

// ChainDone releases a chain the human is finished with: `relevo done <n>`
// where <n> names a chain. Every member that exists is released through the
// ordinary `Done`, builder first, so each member's worktree and process are
// given back the way a lone binding's are; the chain is then marked done with
// one trace row and no MasterMind delivery, because the human ran this verb.
//
// A running chain is refused: `relevo stop <n> first`. A member's failure
// returns its error and leaves the chain's status unchanged, so the verb can be
// run again once the member is fixed.
func ChainDone(ctx context.Context, rt Runtime, name string) (DoneResult, error) {
	c, err := rt.Store.Chain(name)
	if errors.Is(err, store.ErrNotFound) {
		return DoneResult{}, fmt.Errorf("chain %s: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return DoneResult{}, err
	}
	if c.Status == string(chain.StatusRunning) {
		return DoneResult{}, fmt.Errorf("chain %s is running; relevo stop %s first: %w", c.Name, c.Name, ErrChainRunning)
	}

	// The builder's result is the chain's: its worktree and branch are the
	// chain's tree, which is what the verb's document names.
	var out DoneResult
	for i, member := range chainMembersOf(c) {
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
	// The chain is released: its end is done, so the seed copies it kept are no
	// longer needed. The stored row is done whether the verb wrote it now or
	// found it already there.
	chainInputsSweep(rt, name, string(chain.StatusDone))
	return out, nil
}

// chainInputsSweep removes a chain's inputs directory when the chain has ended
// done or stopped: the copies of round files its seeds named are no longer
// needed. A halted chain keeps them -- it may still be resumed -- and the
// chain's own plan-i.md copies, which live beside the directory, are never
// touched. A removal failure is logged, never returned: the transition has
// already been written, and failing it would undo a fact.
func chainInputsSweep(rt Runtime, name, status string) {
	if status != string(chain.StatusDone) && status != string(chain.StatusStopped) {
		return
	}
	if err := os.RemoveAll(rt.Store.ChainInputDir(name)); err != nil {
		slog.Warn("chain inputs sweep", "chain", name, "status", status, "err", err)
	}
}

// chainDoneRow is the chain's own close: status done, phase finished, and one
// trace row naming the member the release was carried out from. No payload is
// queued -- the human ran this verb, so there is nobody to tell.
func chainDoneRow(rt Runtime, tx *store.Tx, c db.ChainRow) error {
	before, err := chainStateOf(c)
	if err != nil {
		return err
	}
	next := before
	next.Status = chain.StatusDone
	next.Phase = chain.PhaseFinished

	member, carrier, ok, err := chainDeliveryMember(tx, c)
	if err != nil {
		return err
	}
	part := ""
	round := 0
	if ok {
		part = chainPartOf(c, member)
		round = carrier.Round
	}
	ev := chain.Event{Kind: chain.EventNeedsYou, Member: part, Round: round, Reason: chainDoneReason}
	act := chain.Action{Kind: chain.ActionFinish}
	return chainSaveWithTrace(rt, tx, c, before, next, ev, act, member)
}
