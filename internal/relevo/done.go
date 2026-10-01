package relevo

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// DoneResult is what Done actually did with the binding's worktree. Exactly
// one of the three path fields is set, or none when the binding has no
// Worktree (a --cwd or adopted binding). Same shape and meaning as the
// worktree fields of UnbindResult.
type DoneResult struct {
	WorktreeRemoved string // path relevo removed; the branch survives.
	WorktreeKept    string // path relevo left in place.
	KeptReason      string // why; "" unless WorktreeKept is set.
	WorktreeGone    string // recorded path that no longer exists.
	Branch          string // b.Branch, for the message; may be "".
}

// Done stops relaying for a binding once the mastermind has verified the work,
// and gives a clean worktree back.
func Done(ctx context.Context, rt Runtime, name string) (DoneResult, error) {
	// Load-modify-save, so it runs inside the state lock: the daemon rewrites
	// this binding on every tick and would otherwise resurrect it by saving a
	// pre-Done snapshot back over the top. Reach state only through tx here --
	// rt.Store.Load/Save would try to take the lock a second time and Go
	// mutexes are not reentrant.
	var out DoneResult
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		// A running chain owns its member's rounds, so done is refused the
		// same way the send path refuses one: a DONE member is neither gone
		// nor NEEDS YOU, and the chain would wait forever for a close that
		// can no longer come. Checked in this critical section, with the
		// state write below, exactly as the send path checks it.
		if err := refuseRunningChainMember(tx, name); err != nil {
			return err
		}

		// A served binding still queued has no process to stop and no
		// completed round to hand back; refuse the same way the wire does, so
		// the server-local `relevo serve` admin verbs agree with it.
		if b.Owner != "" && !b.QueuedAt.IsZero() {
			return fmt.Errorf("round %d is queued; relevo stop to drop it from the queue, or unbind", b.Round)
		}

		// A remote binding's server is told first: the server is the
		// one place that knows whether the round is still open, and it must
		// agree before this binding stops relaying locally.
		//
		// A member of a server chain is the exception: the chain's own verbs
		// release it on the server, and this copy is the mirror. Calling the
		// server here would ask it to release a binding the chain already
		// released, and the refusal would fail a done that has already
		// happened where it matters.
		if b.Builder.Remote() && !serverChainMember(tx, b.Name) {
			if rt.Remote == nil {
				return ErrRemoteUnavailable
			}
			if derr := rt.Remote.Done(ctx, b.Builder.Server, b.Name); derr != nil {
				var httpErr *client.HTTPError
				if errors.As(derr, &httpErr) && httpErr.Status == 409 {
					return fmt.Errorf("round %d is running on %s; relevo stop %s to stop it and keep the binding, or relevo unbind %s to drop it", b.Round, b.Builder.Server, b.Name, b.Name)
				}
				if errors.Is(derr, client.ErrUnreachable) {
					return fmt.Errorf("%s unreachable: %w", b.Builder.Server, derr)
				}
				return fmt.Errorf("%s: %w", b.Builder.Server, derr)
			}
		}

		oldState := b.State
		b.State = store.StateDone

		// A headless round's process is stopped here: the
		// mastermind has declared the work finished, so a builder still
		// editing the tree is now the wrong thing. Failure is reported
		// after DONE is saved -- the state change stands either way -- and
		// the pid stays on the endpoint so the human can find it.
		pid, stopErr := stopProcess(ctx, rt, b, "done")
		if stopErr == nil {
			b.Builder = clearProcess(b.Builder)
			if pid != 0 {
				b = abandonSession(b)
			}
		}

		if err := tx.Save(b); err != nil {
			return err
		}

		// A reader binding's throwaway worktree goes with it: done is the
		// human's "this binding is finished", so nothing of it is left behind.
		removeReaderScratch(ctx, rt, b, b.Round)

		// The stop is recorded in the ledger, in the same shape relevo stop
		// uses (closeStopped): the log marker it used to write is gone, and
		// the round's own record of why the process went away is here.
		if stopErr == nil && pid != 0 {
			if err := tx.AppendLog(b.Name, store.LogEntry{
				TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind,
				Kind: store.KindStop, Note: "stopped/done", Confirmed: true,
			}); err != nil {
				return err
			}
		}

		if rt.Hooks != nil && oldState != store.StateDone {
			rt.Hooks.Dispatch(ctx, hooks.Event{
				Type:      hooks.EventStateChanged,
				BindingID: b.Name,
				State:     string(store.StateDone),
				OldState:  string(oldState),
				Round:     b.Round,
				Timestamp: rt.Now().UTC(),
			})
		}

		out.Branch = b.Branch
		switch {
		case b.Worktree == "":
			// nothing
		case b.Builder.Headless() && stopErr != nil:
			out.WorktreeKept = b.Worktree
			out.KeptReason = "builder process still running"
		case !b.Builder.Headless() && !b.RoundStartedAt.IsZero():
			out.WorktreeKept = b.Worktree
			out.KeptReason = fmt.Sprintf("round %d open; the builder may still write", b.Round)
		default:
			outcome := worktreeTeardown(ctx, rt, b, false)
			out.WorktreeRemoved = outcome.Removed
			out.WorktreeKept = outcome.Kept
			out.KeptReason = outcome.Reason
			out.WorktreeGone = outcome.Gone
		}

		if stopErr != nil {
			return fmt.Errorf("%s marked done, but its builder process %d is still running: %v: %w", b.Name, pid, stopErr, ErrStopFailed)
		}
		return nil
	})
	if err == nil {
		// Outside the lock, and logged only: a delete never changes Done's
		// result.
		reapAbandoned(ctx, rt, name)
	}
	return out, err
}
