package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainTerminalWF ends a workflow chain: it writes the status and the trace
// row, then queues exactly one end delivery on the first member whose record
// still exists, in chain_member order. A later tick's close is ignored by the
// engine, so the payload exists once. A fork child is the one end that
// delivers nothing: its end is the parent's child_ended instead.
func chainTerminalWF(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before, next workflow.State, ev workflow.Event, act workflow.Action) error {
	carrier, found, err := chainTerminalCarrier(tx, c)
	if err != nil {
		return err
	}
	closing := carrier.Name
	if err := chainSaveFlow(rt, tx, c, def, before, next, ev, act, closing); err != nil {
		return err
	}
	chainInputsSweep(rt, c.Name, string(next.Status))
	if c.Parent != "" {
		return chainForkChildEnded(ctx, rt, tx, c, next)
	}
	if !found {
		slog.Warn("chain ended with no member record to deliver on", "chain", c.Name, "status", string(next.Status))
		return nil
	}
	if chainServedCarrier(carrier) {
		return nil
	}
	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: carrier.Round,
		Direction: store.DirToMasterMind, Kind: store.KindChain,
		Payload: chainTerminalWFPayload(c, next),
	}
	return delivery.Queue(ctx, deliveryDeps(rt), tx, carrier.Name, entry)
}

// chainTerminalCarrier names the member a chain's end is recorded against: the
// first member whose record still exists, in chain_member order. A fork child
// names none -- it looks no member up and carries no delivery, only the
// handoff to its parent.
func chainTerminalCarrier(tx *store.Tx, c db.ChainRow) (store.Binding, bool, error) {
	if c.Parent != "" {
		return store.Binding{}, false, nil
	}
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return store.Binding{}, false, err
	}
	for _, m := range rows {
		b, lerr := tx.Load(m.Binding)
		if errors.Is(lerr, store.ErrNotFound) {
			continue
		}
		if lerr != nil {
			return store.Binding{}, false, lerr
		}
		return b, true, nil
	}
	return store.Binding{}, false, nil
}

// chainTerminalWFPayload is what the mastermind reads when a workflow chain
// ends: how it ended, where it got to, and the command that resumes it (on a
// halt) or shows its trace.
func chainTerminalWFPayload(c db.ChainRow, s workflow.State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "chain %s %s: status %s, at %s, plans %d/%d, findings %d.",
		c.Name, chainTerminalVerb(chain.Status(s.Status)), string(s.Status), flowTerminalStep(s), flowPlanPos(s), flowPlanTotal(s), flowFindings(s))
	if s.Reason != "" {
		fmt.Fprintf(&b, " Reason: %s.", s.Reason)
	}
	if s.Status == workflow.StatusHalted {
		fmt.Fprintf(&b, " relevo chain --resume --name %s resumes it.", c.Name)
	}
	fmt.Fprintf(&b, " Branch %s. relevo show %s --trace", c.Branch, c.Name)
	return b.String()
}

// flowTerminalStep is the step a terminal state names: the step it halted at,
// or the one the engine is on.
func flowTerminalStep(s workflow.State) string {
	if s.At != "" {
		return s.At
	}
	return s.Awaiting.Step
}

// flowPlanTotal is a state's plan-input length.
func flowPlanTotal(s workflow.State) int {
	return len(s.Iter["plans"].Items)
}
