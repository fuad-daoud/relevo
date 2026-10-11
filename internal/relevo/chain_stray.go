package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainStrayRound is the round of a member that a finished chain left open: a
// prompt went out and no report ever came back, on a chain that has already
// ended. Nothing will ever close it, so `history` reads it as a round still in
// flight long after the chain stopped.
//
// A round with no prompt was never handed out and is not a stray. A round with
// a report closed and is not a stray. Both are left exactly as they are.
func chainStrayRound(entries []store.LogEntry, round int) bool {
	return store.RoundOpen(entries, round)
}

// chainCloseStrays closes every stray round the chain's members hold, so a chain
// that has ended leaves no open round behind it.
//
// A halted chain is deliberately not swept: a halt is the state a resume sends
// the next round from, so its open round is the awaited one and closing it would
// strand the member. Only a chain that will never run another round -- done or
// stopped -- is swept, and each close is filed under the round it closes, so the
// trace still names which round was abandoned.
func chainCloseStrays(rt Runtime, tx *store.Tx, c db.ChainRow) error {
	switch chain.Status(c.Status) {
	case chain.StatusDone, chain.StatusStopped:
	default:
		return nil
	}
	for _, member := range chainMembersOf(c) {
		if err := chainCloseMemberStrays(rt, tx, c.Name, member); err != nil {
			return err
		}
	}
	return nil
}

// chainCloseMemberStrays closes one member's open rounds, leaving the member at
// the round it closed: a stray is a round nobody will finish, so the member does
// not advance past it and does not keep a start stamp for it.
func chainCloseMemberStrays(rt Runtime, tx *store.Tx, chainName, member string) error {
	b, err := tx.Load(member)
	if err != nil {
		return nil // a member whose record is gone has no round to leave open
	}
	entries, err := tx.ReadLog(member)
	if err != nil {
		return err
	}
	var closed int
	for r := 1; r <= b.Round; r++ {
		if !chainStrayRound(entries, r) {
			continue
		}
		if err := tx.AppendLog(member, store.LogEntry{
			TS: rt.Now().UTC(), Round: r, Direction: store.DirToMasterMind,
			Kind: store.KindStop, Note: "stopped/abandoned", Confirmed: true,
		}); err != nil {
			return err
		}
		if err := tx.AppendLog(member, store.LogEntry{
			TS: rt.Now().UTC(), Round: r, Direction: store.DirToMasterMind,
			Kind: store.KindReport, Note: chainStrayNote(chainName, r),
			Outcome: "halted", Confirmed: true,
		}); err != nil {
			return err
		}
		// The round is closed, so nothing may still read as waiting on it: the
		// start stamp is what `status` and the queue read a live round from.
		b.RoundStartedAt = time.Time{}
		b.QueuedAt = time.Time{}
		closed++
	}
	if closed == 0 {
		return nil
	}
	return tx.Save(b)
}

// chainStrayNote names why a round closed without a builder ever reporting, so
// the log says what happened instead of leaving the round unexplained.
func chainStrayNote(chainName string, round int) string {
	return fmt.Sprintf("round %d was never reported: chain %s ended while it was open", round, chainName)
}

// chainStraySweep is the sweep a pull runs for a chain whose view it just read.
// It runs under the caller's lock and after the row is written, so a chain the
// pull has just seen finish is swept on the same pass that recorded the finish.
func chainStraySweep(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) {
	if err := chainCloseStrays(rt, tx, c); err != nil {
		slog.Warn("chain stray round sweep", "chain", c.Name, "err", err)
	}
}
