package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// tickChains sweeps every running chain once per tick. A member whose record
// is gone, or that sits NEEDS YOU, halts the chain with that member's reason:
// no member close will ever arrive to move the chain, so the sweep is what
// ends it. The daemon runs the sweep before it acts on the binding list,
// because the chain that most needs it is the one whose member records have
// all gone.
func tickChains(ctx context.Context, rt Runtime) {
	chains, err := rt.Store.Chains()
	if err != nil {
		slog.Warn("chain sweep: list chains", "err", err)
		return
	}
	var running []string
	for _, c := range chains {
		if c.Status != string(chain.StatusRunning) {
			continue
		}
		// A server chain is swept by the server's daemon; this machine must
		// not halt it from a mirror that has not been pulled.
		if chainOnServer(c) {
			continue
		}
		running = append(running, c.Name)
	}
	if len(running) == 0 {
		return
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, name := range running {
			if err := chainSweep(ctx, rt, tx, name); err != nil {
				slog.Warn("chain sweep", "chain", name, "err", err)
			}
		}
		return nil
	}); err != nil {
		slog.Warn("chain sweep", "err", err)
	}
}

// chainSweep decides one chain under the caller's lock. The row is re-read
// here, so a sweep that races a close writes nothing on a chain that has
// already ended. Member order is fixed: the builder is checked first, so the
// reason the human sees names the member the chain is closest to acting on.
func chainSweep(ctx context.Context, rt Runtime, tx *store.Tx, name string) error {
	c, err := tx.Chain(name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Status != string(chain.StatusRunning) {
		return nil
	}
	return chainSweepFlow(ctx, rt, tx, c)
}

// chainSweepFlow decides a workflow chain: it walks chain_member in creation
// order, and the first member whose record is gone, that sits NEEDS YOU, or
// that is broken with nothing able to bring its builder back, ends the chain
// with that member's reason. A chain member is the binding a step's actor runs
// on, so the workflow's members are read from chain_member, not from the four
// legacy columns.
//
// Broken is an arm of its own because the binding a member's break leaves is
// not one any later round can move: a member whose builder is gone and has no
// live process behind it has no close coming, so a chain waiting on it waits
// forever. Its owner is a mirror of this chain -- the member observes the round
// and writes the status word, and leaves the fault to the chain, whose end the
// pull delivers once -- so nothing on the owner's side ends the chain, and the
// sweep here is the only thing that can.
func chainSweepFlow(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) error {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return err
	}
	for _, m := range rows {
		mb, lerr := tx.Load(m.Binding)
		if errors.Is(lerr, store.ErrNotFound) {
			return chainSweepFlowHalt(ctx, rt, tx, c, fmt.Sprintf("member %s gone", m.Binding))
		}
		if lerr != nil {
			return lerr
		}
		switch {
		case mb.State == store.StateNeedsYou:
			return chainSweepFlowHalt(ctx, rt, tx, c, mb.Halt)
		case mb.State == store.StateBroken && !bindingSwitchable(mb):
			return chainSweepFlowHalt(ctx, rt, tx, c, chainSweepBrokenReason(m.Binding, mb.Halt))
		}
	}
	return nil
}

// chainSweepBrokenReason is the reason a chain ends on a broken member: the
// member's own halt when it recorded one, and the member named as broken when
// it did not. A break with no reason is reachable -- the field is only written
// once a switch has something to say -- and an empty chain reason is a halted
// row with nothing on it to act on.
func chainSweepBrokenReason(name, halt string) string {
	if halt != "" {
		return halt
	}
	return fmt.Sprintf("member %s broken", name)
}

// chainSweepFlowHalt ends a workflow chain the sweep found unable to move: the
// engine's halt state, one trace row and the one end delivery, through the
// workflow terminal so the chain's own payload and resume hint are written. The
// step named is the one the engine is on, which is where the chain stopped.
func chainSweepFlowHalt(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, reason string) error {
	def, err := chainWorkflowDef(c)
	if err != nil {
		return err
	}
	before, err := chainWorkflowState(c)
	if err != nil {
		return err
	}
	step := flowTerminalStep(before)
	next := before
	next.Status = workflow.StatusHalted
	next.Reason = reason
	ev := workflow.Event{Kind: workflow.EventNeedsYou, Step: step, Reason: reason}
	act := workflow.Action{Kind: workflow.ActionHalt, Step: step, Reason: reason}
	return chainTerminalWF(ctx, rt, tx, c, def, before, next, ev, act)
}
