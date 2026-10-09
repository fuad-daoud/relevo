package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// DoneResult is what Done actually did with the binding's worktree. Exactly
// one of the three path fields is set, or none when the binding has no
// Worktree (a --cwd or adopted binding). Same shape and meaning as the
// worktree fields of UnbindResult.
//
// Payload is what Done delivered on the way out: every unconfirmed
// to-MasterMind entry that was still pending when the human ran `relevo done`,
// rendered and joined exactly as `relevo wait` renders it. Empty when
// nothing was pending.
//
// Joined, and not the per-entry split wait carries, so DoneResult stays
// comparable with == the way it was before this field existed.
type DoneResult struct {
	WorktreeRemoved string // path relevo removed; the branch survives.
	WorktreeKept    string // path relevo left in place.
	KeptReason      string // why; "" unless WorktreeKept is set.
	WorktreeGone    string // recorded path that no longer exists.
	Branch          string // b.Branch, for the message; may be "".
	Payload         string // entries delivered here; "" when none were pending.
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
	// claimed are the entries the lock confirmed above; their text is rendered
	// after the lock, because PushText reads the report file off disk and no
	// file I/O belongs under the state lock.
	var claimed []store.PendingEntry
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

		// `done` is the last chance this binding has to hand its MasterMind
		// something: after it, reconcile's DONE gate only admits what a push
		// route already took and `relevo wait` answers `gone` without pulling,
		// so an entry left unconfirmed here is stranded for good -- a findings
		// entry, a halt, a report the human finished reading before running
		// done. They are claimed and confirmed with route "done" below, in
		// wait's own pull-then-confirm shape and against the same read wait
		// uses, so both paths agree on what pending means and what it becomes.
		//
		// A chain entry is NOT one of them: it is the chain's own status
		// record, written under a binding named after the chain, and `chain
		// done` deliberately queues no MasterMind delivery for it (the human
		// ran the verb). Sweeping it here would consume that record on a plain
		// `relevo done` of one member, so it is skipped by the same kind rule
		// PushText applies.
		//
		// Best effort, like wait's own delivery: a Done that has already
		// stopped the server must not be undone by a payload that would not
		// read. The entries stay pending on a failure, which is the state this
		// verb existed to leave behind -- the human can run `relevo wait` first.
		all, perr := tx.ClaimableForMasterMindThrough(name, 0)
		if perr != nil {
			slog.Warn("could not list pending entries at done", "binding", name, "err", perr)
		}
		for _, p := range all {
			if p.Entry.Kind == store.KindChain {
				continue
			}
			if err := tx.ConfirmIndex(name, p.Idx, "done"); err != nil {
				slog.Warn("could not confirm an entry delivered at done", "binding", name, "idx", p.Idx, "err", err)
			} else {
				claimed = append(claimed, p)
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
	if len(claimed) > 0 {
		// wait's own rendering, in wait's own order: report entries last, so a
		// reader is handed the round's findings rather than a halt that closed
		// them. PushText is the same expansion, and the same truncate.
		binding := delivery.BindingFor(rt.Store, name)
		delivered := make([]delivery.Delivered, 0, len(claimed))
		for _, p := range claimed {
			text, _ := delivery.PushText(p.Entry, binding, rt.Store.ReadFile)
			delivered = append(delivered, delivery.Delivered{Entry: p.Entry, Text: text})
		}
		out.Payload = delivery.JoinDelivered(delivery.ReportLast(delivered))
	}
	return out, err
}
