package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
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
		if c.Status == string(chain.StatusRunning) {
			running = append(running, c.Name)
		}
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

	parts := []string{chain.MemberBuilder, chain.MemberReviewer, chain.MemberPlanner, chain.MemberSecurity}
	for _, part := range parts {
		member := chainMemberName(c, part)
		if member == "" {
			continue
		}
		m, err := tx.Load(member)
		if errors.Is(err, store.ErrNotFound) {
			return chainSweepHalt(ctx, rt, tx, c, part, member, fmt.Sprintf("member %s gone", member))
		}
		if err != nil {
			return err
		}
		if m.State == store.StateNeedsYou {
			return chainSweepHalt(ctx, rt, tx, c, part, member, m.Halt)
		}
	}
	return nil
}

// chainSweepHalt ends a chain the sweep found unable to move: the status, one
// trace row and the one end delivery, all through chainTerminal. The halt
// status is written under the same lock the decision was made in, so a second
// tick re-reads a terminal chain and writes nothing more.
func chainSweepHalt(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, part, member, reason string) error {
	before, err := chainStateOf(c)
	if err != nil {
		return err
	}
	next := before
	next.Status = chain.StatusHalted
	next.Reason = reason

	ev := chain.Event{Kind: chain.EventNeedsYou, Member: part, Round: before.Awaiting.Round, Reason: reason}
	act := chain.Action{Kind: chain.ActionHalt, Reason: reason}
	return chainTerminal(ctx, rt, tx, c, before, next, ev, act, member)
}
