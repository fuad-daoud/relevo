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
	return HasPromptEntry(entries, b.Round) && !HasEntry(entries, b.Round, store.DirToMasterMind, store.KindReport)
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
// is set, the chain's end payload is pulled off whichever member holds it -- the
// first of builder, reviewer, planner and security whose record exists, the
// same order chainTerminal queued it in; a delivery failure is returned in
// DeliverErr and never changes the exit.
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
	// A member whose record is gone has no log, so PullPendingThrough finds
	// nothing there and confirms nothing: the walk reaches the surviving
	// member that carries the delivery.
	for _, member := range chainMembersOf(c) {
		text, found, err := delivery.PullPendingThrough(ctx, rt.Store, member, "wait", 0)
		if err != nil {
			res.DeliverErr = err
			return res, nil
		}
		if found {
			res.Payload = text
			return res, nil
		}
	}
	return res, nil
}

// chainEndLine is the line `relevo wait` prints for a chain that has stopped
// running, in the shape a chain row uses: its name and status, the plan it
// reached and the correction rounds it spent, plus the halt reason.
func chainEndLine(c db.ChainRow) string {
	line := fmt.Sprintf("chain %s: %s · plan %d/%d · %d corrections", c.Name, c.Status, c.Plan, c.Plans, c.Corrections)
	if c.Reason != "" {
		line += " · " + c.Reason
	}
	return line
}
