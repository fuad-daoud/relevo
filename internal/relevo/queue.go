package relevo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNotQueued reports that Admit was asked to start a round that is not
// waiting in the queue: QueuedAt is zero, or the binding is not active
// (#285).
var ErrNotQueued = errors.New("round is not queued")

// Admit starts a queued round's builder: the counterpart to Send(Defer),
// called by serve.admit once a slot under serve.max_builders frees up
// (#285). It spawns the process (switching candidate first if the queued
// one is now gated), stamps RoundStartedAt, zeroes QueuedAt, and logs the
// wait as a KindQueue entry -- the audit trail for how long the round sat.
//
// The stamp and the note are written only for a start that actually produced a
// process; the admission itself always happens. A switch whose replacement
// could not be resolved leaves the binding broken with nothing running, and
// recording that as a started round is what used to hide the fault.
func Admit(ctx context.Context, rt Runtime, name string) error {
	var b store.Binding
	admitted := false

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		loaded, err := tx.Load(name)
		if err != nil {
			return err
		}
		if loaded.QueuedAt.IsZero() || loaded.State != store.StateActive {
			return ErrNotQueued
		}
		b = loaded

		prompt := roundPrompt(rt, tx, b, rt.Store.PromptPath(name, b.Round), rt.Store.ReportPath(name, b.Round), rt.Store.DonePath(name, b.Round))

		// Add the oom note because the worktree may hold partial work.
		if b.OOMRequeue != nil {
			prompt += "\n\n" + oomNote(b.OOMRequeue.At)
			b.OOMRequeue = nil
		}

		reason := ""
		if _, gated := gatedBuilder(rt, b); gated {
			reason = "gated while queued"
		} else if staleBuilder(rt, b) {
			reason = "candidate " + b.BuilderCandidate + " is no longer configured"
		}

		var switched bool
		var startErr error
		// A queued reader round runs in its scratch worktree (A5 §2): create it
		// before the admit spawns anything, from the baseline the deferred Send
		// captured. A failure leaves the binding NEEDS YOU below, like any
		// other admit failure, and never falls back to b.CWD.
		if b.Shape == store.ShapeReader {
			if _, err := CreateScratchFrom(ctx, rt, b, b.Round, b.RoundBaselineHead, b.RoundBaselineTree); err != nil {
				startErr = err
			}
		}
		if startErr == nil {
			if reason != "" {
				switched = true
				b, startErr = switchBuilder(ctx, rt, tx, b, reason, false /*closeOld*/, false /*counted*/)
			} else {
				b, startErr = startRound(ctx, rt, tx, b, prompt, false)
			}
		}

		if startErr != nil {
			// Mirror Send's own spawn-failure handling -- the round stays open
			// (the plan was already staged when it was deferred), nothing
			// started, so a human has to act -- but halt through the shared
			// path rather than by hand, so the failure owes its MasterMind the
			// one halt entry every other NEEDS YOU owes. Send can return the
			// error to a caller that reports it; Admit runs in the daemon, and
			// a MasterMind with no push route heard nothing at all before.
			// haltAndSettle also attempts the delivery, in this same critical
			// section, exactly as a reconcile halt does.
			//
			// A binding that came back already halted keeps its reason: the
			// switch paths halt with a fuller text than "spawn failed", and
			// the entry carrying it is already queued. What says so is the
			// notification, not the presence of a reason -- a halt whose entry
			// the log refused reads as carrying a reason with nothing behind
			// it, and saving that leaves the round active with nothing running
			// and no halt anyone was told about. A binding the switch never
			// finished keeps State Active and falls through to the halt below.
			if b.State == store.StateNeedsYou && b.HaltNotifiedRound == b.Round {
				b.QueuedAt = time.Time{}
				if saveErr := tx.Save(b); saveErr != nil {
					return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", startErr, saveErr)
				}
				return startErr
			}
			halted, herr := haltAndSettle(ctx, rt, tx, b, "builder spawn failed: "+startErr.Error())
			if herr != nil {
				return fmt.Errorf("%v; and halting the round failed: %w", startErr, herr)
			}
			// QueuedAt is zeroed so the round is never re-admitted.
			halted.QueuedAt = time.Time{}
			if saveErr := tx.Save(halted); saveErr != nil {
				return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", startErr, saveErr)
			}
			return startErr
		}

		age := rt.Now().Sub(b.QueuedAt).Round(time.Second)
		b.QueuedAt = time.Time{}

		// A start that did not happen is not stamped and not announced.
		// switchBuilder can come back from the switch branch with no error and
		// no process: when the replacement's own resolve failed it queued this
		// binding's halt entry, delivered it and returned. RoundStartedAt is
		// what both view.WaitingOn and queueBrokenHalt read to mean "the daemon
		// will fix this itself", so stamping it here would flip a broken
		// binding with no process back to switchable -- and the KindQueue note
		// would tell the MasterMind a round began when none did.
		//
		// QueuedAt is zeroed either way: the round was admitted and did not
		// run, and leaving it queued would retry the same failed switch on
		// every tick. The queued halt entry stands on its own, and wait pulls
		// it.
		if b.Builder.PID == 0 {
			return tx.Save(b)
		}

		note := fmt.Sprintf("started after %s queued", age)
		if switched {
			note = fmt.Sprintf("started after %s queued (switched: %s)", age, reason)
		}
		b.RoundStartedAt = rt.Now()
		if err := tx.AppendLog(name, store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true,
			Note: note,
		}); err != nil {
			return err
		}
		admitted = true
		return tx.Save(b)
	})
	if err != nil {
		return err
	}

	if admitted && rt.Hooks != nil {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventRoundAdmitted,
			BindingID: name,
			State:     string(b.State),
			Round:     b.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	return nil
}
