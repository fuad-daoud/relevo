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

		prompt := composePrompt(rt, b, rt.Store.PlanPath(name, b.Round), rt.Store.ReportPath(name, b.Round), rt.Store.DonePath(name, b.Round))

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
				b, startErr = startRound(ctx, rt, tx, b, prompt)
			}
		}

		if startErr != nil {
			// Mirror Send's own spawn-failure handling: the round stays open
			// (the plan was already staged when it was deferred), but nothing
			// started, so a human has to act. QueuedAt is zeroed so the round
			// is never re-admitted.
			b.State = store.StateNeedsYou
			b.Halt = "builder spawn failed: " + startErr.Error()
			b.HaltAt = rt.Now().UTC()
			b.QueuedAt = time.Time{}
			if saveErr := tx.Save(b); saveErr != nil {
				return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", startErr, saveErr)
			}
			return startErr
		}

		age := rt.Now().Sub(b.QueuedAt).Round(time.Second)
		note := fmt.Sprintf("started after %s queued", age)
		if switched {
			note = fmt.Sprintf("started after %s queued (switched: %s)", age, reason)
		}
		b.RoundStartedAt = rt.Now()
		b.QueuedAt = time.Time{}
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
