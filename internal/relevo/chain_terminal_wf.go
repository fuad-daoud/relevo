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
// engine, so the payload exists once.
func chainTerminalWF(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before, next workflow.State, ev workflow.Event, act workflow.Action) error {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return err
	}
	var carrier store.Binding
	found := false
	for _, m := range rows {
		b, lerr := tx.Load(m.Binding)
		if errors.Is(lerr, store.ErrNotFound) {
			continue
		}
		if lerr != nil {
			return lerr
		}
		carrier, found = b, true
		break
	}
	if err := chainSaveFlow(rt, tx, c, def, before, next, ev, act, carrier.Name); err != nil {
		return err
	}
	chainInputsSweep(rt, c.Name, string(next.Status))
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

// chainTerminalWFPayload is what the mastermind reads when a workflow chain
// ends: how it ended, where it got to, and the command that resumes it (on a
// halt) or shows its trace.
func chainTerminalWFPayload(c db.ChainRow, s workflow.State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "chain %s %s: status %s, at %s, plans %d/%d.",
		c.Name, chainTerminalVerb(chain.Status(s.Status)), string(s.Status), flowTerminalStep(s), flowPlanPos(s), flowPlanTotal(s))
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
