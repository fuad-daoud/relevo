package relevo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ChainWaitTarget decides what `relevo wait --name <n>` waits on when the
// resolved target is a chain. A running chain always waits on the chain: its
// end is the one event the caller asked about. A chain that is no longer
// running usually waits on the chain too -- its halt or finish is the answer --
// but when the chain's builder member has a round open (a prompt entry for its
// current round with no report yet, or a queued round) a human's manual round
// is in flight, and the wait must follow that round the way it follows any
// binding.
//
// It returns the binding wait should target and whether the chain arm should be
// used instead. A name that is no chain is the ordinary binding path. It is
// read-only.
func ChainWaitTarget(rt Runtime, name string) (binding string, chainArm bool, err error) {
	c, err := rt.Store.Chain(name)
	if errors.Is(err, store.ErrNotFound) {
		return name, false, nil
	}
	if err != nil {
		return name, false, err
	}
	if chain.Status(c.Status) == chain.StatusRunning {
		return name, true, nil
	}
	if chainBuilderRoundOpen(rt.Store, c) {
		return c.Builder, false, nil
	}
	return name, true, nil
}

// chainBuilderRoundOpen reports whether a chain's builder member has a round
// open: a prompt entry for its current round with no report entry for it yet,
// or a queued round that has not started. A builder record that is gone, or
// unreadable, has no open round.
func chainBuilderRoundOpen(s *store.Store, c db.ChainRow) bool {
	b, err := s.Load(c.Builder)
	if err != nil {
		return false
	}
	if !b.QueuedAt.IsZero() {
		return true
	}
	entries, err := s.ReadLog(c.Builder)
	if err != nil {
		return false
	}
	return store.RoundOpen(entries, b.Round)
}

// WaitChain polls a chain until it is no longer running and reports how it
// ended. A finished chain is exit 0; a halted or stopped one is exit 3, the
// code a chain that waits on a human shares with every other binding that
// needs one. Unless peek is set, the chain's one end delivery -- queued on the
// builder when the chain ended -- is pulled with route "wait" and returned as
// the Payload; a chain that was already delivered has none.
//
// A name the store holds no chain for is store.ErrNotFound, wrapped, which the
// CLI reports as a binding that does not exist. A timeout is WaitTimeout.
// interval is the poll period, passed so a test never sleeps for a second.
func WaitChain(ctx context.Context, rt Runtime, name string, timeout, interval time.Duration, peek bool) (WaitResult, error) {
	if timeout <= 0 {
		return WaitResult{}, fmt.Errorf("wait: timeout must be positive")
	}
	if interval <= 0 {
		return WaitResult{}, fmt.Errorf("wait: interval must be positive")
	}
	// A chain that runs on a server waits by pulling the mirror; a name that
	// is no chain, or a local one, keeps the loop below.
	if c, err := rt.Store.Chain(name); err == nil && chainOnServer(c) {
		return chainServerWait(ctx, rt, name, timeout, interval, peek)
	}

	start := rt.Now()
	for {
		c, err := rt.Store.Chain(name)
		if errors.Is(err, store.ErrNotFound) {
			return WaitResult{}, fmt.Errorf("chain %s: %w", name, store.ErrNotFound)
		}
		if err != nil {
			return WaitResult{}, err
		}
		if chain.Status(c.Status) != chain.StatusRunning {
			return waitChainEnd(ctx, rt, c, peek)
		}
		if rt.Now().Sub(start) >= timeout {
			return WaitResult{Code: WaitTimeout, Done: true}, nil
		}

		select {
		case <-ctx.Done():
			return WaitResult{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// waitChainEnd classifies a chain that has stopped running: done is exit 0 and
// halted or stopped is exit 3, with the line naming the state, the plan it
// reached and the corrections it spent. A halt carries its reason. Unless peek
// is set, the chain's end payloads are pulled off the members that hold them;
// a delivery failure is returned in DeliverErr and never changes the exit.
func waitChainEnd(ctx context.Context, rt Runtime, c db.ChainRow, peek bool) (WaitResult, error) {
	res := WaitResult{Done: true, Line: chainEndLine(c)}
	if c.Status == string(chain.StatusDone) {
		res.Code = WaitClosed
	} else {
		res.Code = WaitNeedsYou
	}
	if peek {
		return res, nil
	}
	delivered, err := chainEndEntries(ctx, rt, c)
	if err != nil {
		res.DeliverErr = err
		return res, nil
	}
	if len(delivered) == 0 {
		return res, nil
	}
	res.Delivered = delivered
	res.Payload = delivery.JoinDelivered(delivered)
	return res, nil
}

// chainEndEntries collects every payload the chain's members still owe the
// MasterMind and answers them in the order the end result reads them.
//
// Every member is walked, not the first one with a payload: the chain's one end
// delivery is not the only thing a member can owe it, and a halt on a later
// member -- a reviewer that timed out, a security member that found something
// -- is the most urgent fact on the chain. Stopping at the first member holding
// anything would leave that halt pending for a reader that never comes. A
// member whose record is gone has no log, so the pull finds nothing there and
// confirms nothing: the walk reaches the surviving member that carries the
// delivery.
func chainEndEntries(ctx context.Context, rt Runtime, c db.ChainRow) ([]delivery.Delivered, error) {
	var collected []delivery.Delivered
	for _, member := range chainReadMembers(rt.Store, c) {
		entries, err := delivery.PullPendingThroughEntries(ctx, rt.Store, member, "wait", 0)
		if err != nil {
			return nil, err
		}
		collected = append(collected, entries...)
	}
	return chainHaltsFirst(collected), nil
}

// chainHaltsFirst is the precedence the end result is ordered by: every halt
// entry leads, then the other entries, each group in the order the members were
// walked. A halt is the one fact on a chain a human has to act on, so a halt on
// a later member must not sit behind an ordinary report from an earlier one.
//
// Nothing is dropped for the halt: the entries after it are still delivered, so
// a chain holding both answers with both rather than losing the report the halt
// outranked. Within each group the walk order holds, which is what keeps the
// result the one the walk read.
func chainHaltsFirst(collected []delivery.Delivered) []delivery.Delivered {
	if len(collected) < 2 {
		return collected
	}
	halts := make([]delivery.Delivered, 0, len(collected))
	rest := make([]delivery.Delivered, 0, len(collected))
	for _, d := range collected {
		if d.Entry.Kind == store.KindHalt {
			halts = append(halts, d)
			continue
		}
		rest = append(rest, d)
	}
	return append(halts, rest...)
}

// chainEndLine is the line `relevo wait` prints for a chain that has stopped
// running, in the shape a chain row uses: its name and status, the plan it
// reached and the correction rounds it spent, plus the halt reason.
func chainEndLine(c db.ChainRow) string {
	lf := chainStoredFactsOf(c)
	line := fmt.Sprintf("chain %s: %s · plan %d/%d · %d corrections", c.Name, c.Status, lf.Plan, lf.Plans, lf.Corrections)
	if c.Reason != "" {
		line += " · " + c.Reason
	}
	return line
}
