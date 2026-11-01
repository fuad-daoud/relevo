package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Delivery is the outcome of one delivery attempt. All-false means "not yet,
// try again next tick".
type Delivery struct {
	Delivered bool
	Empty     bool // nothing was pending for this binding
	// Route is how this attempt would deliver, or did. "wait" is a route, not a
	// fault: the entry stays pending for the background wait.
	Route  string
	Reason string
	// Round is the pending entry's round, set on Delivered and on a left-
	// pending attempt so the caller can log the outcome without re-reading
	// the queue.
	Round int
}

// Queue records a mastermind-bound payload as pending before any delivery, so
// a crash before confirmation restarts as unconfirmed. It takes the caller's
// tx: Reconcile appends inside the same critical section as its round advance.
func Queue(_ context.Context, d Deps, tx *store.Tx, name string, e store.LogEntry) error {
	if e.Direction != store.DirToMasterMind {
		return fmt.Errorf("queue expects a mastermind-bound entry, got %q", e.Direction)
	}
	if e.Payload == "" {
		return fmt.Errorf("queue expects a payload for binding %q", name)
	}
	if e.TS.IsZero() {
		e.TS = d.Now().UTC()
	}
	e.Confirmed = false
	e.Payload = WithOrigin(e.Payload, OriginLine(name, e.Round, e.Direction, e.Kind))

	return tx.AppendLog(name, e)
}

// DeliverPending attempts the oldest pending payload for one binding against
// the routes, in order:
//
//  1. the push claim: the holder's own loop writes and confirms the entry, so
//     this returns without touching the log.
//  2. d.Deliverers[b.MasterMind.Kind]: OutcomeNotMine leaves the entry pending
//     with its reason; there is no pane to fall through to.
//  3. otherwise the entry stays pending with Route "pull": the normal path for
//     a tools-mode mastermind, collected by the background wait.
//
// An admitted entry is only read back, never pushed again, and an orphaned
// admit (dead holder, no deliverer) is cleared here so it is claimable again.
//
// The caller holds the state lock across pending -> deliver -> confirm and
// passes tx in: `relevo wait` runs the same sequence from another process.
func DeliverPending(ctx context.Context, d Deps, tx *store.Tx, b store.Binding) (store.Binding, Delivery, error) {
	pending, idx, found, err := tx.PendingForMasterMind(b.Name)
	if err != nil {
		return b, Delivery{}, err
	}
	if !found {
		return b, Delivery{Empty: true, Reason: "nothing pending"}, nil
	}

	if err := clearOrphanAdmit(d, tx, b, pending, idx); err != nil {
		return b, Delivery{}, err
	}

	if d.Channels != nil && b.MasterMindID != "" {
		c, err := d.Channels.Live(b.MasterMindID, d.Now())
		if err != nil {
			return b, Delivery{}, fmt.Errorf("push claim: %w", err)
		}
		if c != nil {
			return b, Delivery{Route: "push", Reason: "mastermind has a push claim", Round: pending.Round}, nil
		}
	}

	kind := b.MasterMind.Kind
	if del, ok := d.Deliverers[kind]; ok && kind != "" {
		return deliverViaDeliverer(ctx, d, tx, b, pending, idx, del)
	}

	return b, Delivery{
		Route:  "pull",
		Reason: fmt.Sprintf("awaiting pull for mastermind %s (%s)", mastermindLabel(d, b), kind),
		Round:  pending.Round,
	}, nil
}

// ConfirmAdmitted settles an entry admitted before the binding's state changed.
// It reads the route's queue back once and never pushes, so a done or paused
// binding gets no new payload. An unadmitted entry is left untouched, and an
// admit past the horizon is cleared for the background wait before the same
// one read-back runs.
func ConfirmAdmitted(ctx context.Context, d Deps, tx *store.Tx, b store.Binding) (store.Binding, Delivery, error) {
	pending, idx, found, err := tx.PendingForMasterMind(b.Name)
	if err != nil {
		return b, Delivery{}, err
	}
	if !found {
		return b, Delivery{Empty: true, Reason: "nothing pending"}, nil
	}
	if err := clearOrphanAdmit(d, tx, b, pending, idx); err != nil {
		return b, Delivery{}, err
	}
	if pending.AdmittedAt == nil {
		return b, Delivery{Route: "pull", Reason: "the pending entry was not admitted", Round: pending.Round}, nil
	}

	kind := b.MasterMind.Kind
	del, ok := d.Deliverers[kind]
	if !ok || kind == "" {
		return b, Delivery{Route: "pull", Reason: fmt.Sprintf("no deliverer for mastermind kind %q", kind), Round: pending.Round}, nil
	}
	if age, expired := admitExpired(d, del, pending); expired {
		// Cleared for the background wait; this path never pushes.
		if err := expireAdmit(tx, b, pending, idx, age); err != nil {
			return b, Delivery{}, err
		}
		// The payload may still have arrived before the horizon, so the same one
		// read-back runs here and confirms rather than sends.
		text, _ := PushText(pending, b, d.Store.ReadFile)
		out, reason, err := del.ConfirmOnce(ctx, b.MasterMind, text, pending.TS)
		if err != nil {
			return b, Delivery{}, fmt.Errorf("confirm to mastermind: %w", err)
		}
		if out == OutcomeDelivered {
			return deliveryOf(tx, b, idx, "deliverer:"+kind, out, reason, pending.Round)
		}
		return b, Delivery{
			Route:  "pull",
			Reason: fmt.Sprintf("the admitted entry expired after %s; it is claimable for the background wait", age),
			Round:  pending.Round,
		}, nil
	}
	route := "deliverer:" + kind
	text, _ := PushText(pending, b, d.Store.ReadFile)
	out, reason, err := del.ConfirmOnce(ctx, b.MasterMind, text, pending.TS)
	if err != nil {
		return b, Delivery{}, fmt.Errorf("confirm to mastermind: %w", err)
	}
	return deliveryOf(tx, b, idx, route, out, reason, pending.Round)
}

// deliverViaDeliverer runs one deliverer attempt: Deliver for a fresh entry,
// Confirm for one this tick admitted, ConfirmOnce when an earlier tick did.
// The read-back runs inside the caller's lock, so no reader claims the entry
// mid-confirm; the admitting tick holds the lock for Confirm's window, a
// repeat tick for one read-back only.
//
// The admit is written before the read-back poll, so a crash during the poll
// reads back instead of resending. An admit past the horizon is the exception:
// it is cleared and the entry is pushed again, or the admit would hold the
// queue forever.
func deliverViaDeliverer(ctx context.Context, d Deps, tx *store.Tx, b store.Binding, pending store.LogEntry, idx int, del MasterMindDeliverer) (store.Binding, Delivery, error) {
	kind := b.MasterMind.Kind
	route := "deliverer:" + kind
	text, _ := PushText(pending, b, d.Store.ReadFile)

	if pending.AdmittedAt != nil {
		age, expired := admitExpired(d, del, pending)
		if !expired {
			out, reason, err := del.ConfirmOnce(ctx, b.MasterMind, text, pending.TS)
			if err != nil {
				return b, Delivery{}, fmt.Errorf("confirm to mastermind: %w", err)
			}
			return deliveryOf(tx, b, idx, route, out, reason, pending.Round)
		}
		// The admit outlived the horizon with no read-back finding the payload, so
		// clearing falls through to Deliver, which reads back before sending.
		if err := expireAdmit(tx, b, pending, idx, age); err != nil {
			return b, Delivery{}, err
		}
	}

	out, reason, err := del.Deliver(ctx, b.MasterMind, text, LogRef(b, pending), pending.TS)
	if err != nil {
		return b, Delivery{}, fmt.Errorf("deliver to mastermind: %w", err)
	}
	if out == OutcomeAdmitted {
		if err := tx.AdmitIndex(b.Name, idx); err != nil {
			return b, Delivery{}, err
		}

		out, reason, err = del.Confirm(ctx, b.MasterMind, text, pending.TS)
		if err != nil {
			return b, Delivery{}, fmt.Errorf("confirm to mastermind: %w", err)
		}
		return deliveryOf(tx, b, idx, route, out, reason, pending.Round)
	}
	if out == OutcomeDelivered {
		return deliveryOf(tx, b, idx, route, out, reason, pending.Round)
	}

	// OutcomeNotMine and OutcomeUnavailable leave the entry pending for the
	// background wait, with the deliverer's own reason.
	return b, Delivery{Route: "pull", Reason: reason, Round: pending.Round}, nil
}

// admitExpired reports whether an entry's admit outlived the route's horizon.
// Without a bound an entry invisible to read-back stays admitted forever: the
// give-up gates all live inside Deliver, which never runs again for an admitted
// entry, and the admit blocks the queue and hides the entry from every reader.
// A zero horizon keeps admits unbounded.
func admitExpired(d Deps, del MasterMindDeliverer, pending store.LogEntry) (time.Duration, bool) {
	if pending.AdmittedAt == nil {
		return 0, false
	}
	horizon := del.AdmitHorizon()
	if horizon <= 0 {
		return 0, false
	}
	age := d.Now().Sub(*pending.AdmittedAt)
	if age <= horizon {
		return 0, false
	}
	return age, true
}

// expireAdmit clears an unsettled admit so the entry is deliverable again,
// logging once per expired admit.
func expireAdmit(tx *store.Tx, b store.Binding, pending store.LogEntry, idx int, age time.Duration) error {
	if err := tx.ClearAdmitIndex(b.Name, idx); err != nil {
		return err
	}
	slog.Info("admitted push was never read back; the admit is cleared and the payload is deliverable again",
		"binding", b.Name, "round", pending.Round, "age", age)
	return nil
}

// deliveryOf turns a deliverer outcome into Delivery: only OutcomeDelivered
// confirms; anything else stays in the push route's queue, never shown to a reader.
func deliveryOf(tx *store.Tx, b store.Binding, idx int, route string, out Outcome, reason string, round int) (store.Binding, Delivery, error) {
	if out != OutcomeDelivered {
		return b, Delivery{Route: route, Reason: reason, Round: round}, nil
	}
	if err := tx.ConfirmIndex(b.Name, idx, route); err != nil {
		return b, Delivery{}, err
	}
	return b, Delivery{Delivered: true, Route: route, Reason: reason, Round: round}, nil
}

// mastermindLabel names the mastermind for a delivery reason: the registry's
// name when known, else the id.
func mastermindLabel(d Deps, b store.Binding) string {
	if b.MasterMindID == "" {
		return b.MasterMind.Kind
	}
	if d.MasterMinds != nil {
		if rec, err := d.MasterMinds.Get(b.MasterMindID); err == nil && rec.Name != "" {
			return rec.Name
		}
	}
	return b.MasterMindID
}
