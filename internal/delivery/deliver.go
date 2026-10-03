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
	// Route is how this attempt would deliver, or did: "channel",
	// "deliverer:<kind>" or "wait". "wait" is a route, not a fault: the entry
	// stays pending for the background wait, which is how a Claude Code
	// mastermind in tools mode gets its report.
	Route  string
	Reason string
	// Round is the pending entry's round, set on Delivered and on a left-
	// pending attempt so the caller can log the outcome without re-reading
	// the queue.
	Round int
}

// Queue records a mastermind-bound payload as pending BEFORE any delivery is
// attempted. A crash between here and confirmation leaves the entry
// unconfirmed, which is exactly how relevo notices it on restart.
//
// It takes the caller's tx rather than locking itself: Reconcile appends this
// entry inside the same critical section as the round advance that follows it,
// and Go mutexes are not reentrant.
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
//  1. the channel: a live claim for b.MasterMindID. The claim holder's own poll
//     pushes the entry and confirms it with route=channel, so this returns
//     without touching the log -- confirming here would empty the mailbox
//     before the channel reader saw it.
//  2. d.Deliverers[b.MasterMind.Kind]: a deliverer that reports OutcomeNotMine
//     leaves the entry pending with its reason; it no longer falls through to
//     a pane (there is none).
//  3. otherwise the entry stays pending with Delivery.Route "pull". For a
//     Claude Code mastermind in tools mode that is the normal path, not a fault:
//     the background wait prints it (its route is "wait").
//
// An entry the deliverer already admitted is no longer a pending one to push:
// it is only read back, so a route that admitted it can never send it twice.
//
// The caller holds the state lock across pending -> deliver -> confirm and
// passes tx in: `relevo wait` runs the same sequence from another process, and
// unserialised both could deliver the same payload, and Reconcile needs this
// step inside the same lock as the rest of one binding's advance.
func DeliverPending(ctx context.Context, d Deps, tx *store.Tx, b store.Binding) (store.Binding, Delivery, error) {
	pending, idx, found, err := tx.PendingForMasterMind(b.Name)
	if err != nil {
		return b, Delivery{}, err
	}
	if !found {
		return b, Delivery{Empty: true, Reason: "nothing pending"}, nil
	}

	if d.Channels != nil && b.MasterMindID != "" {
		c, err := d.Channels.Live(b.MasterMindID, d.Now())
		if err != nil {
			return b, Delivery{}, fmt.Errorf("channel claim: %w", err)
		}
		if c != nil {
			return b, Delivery{Route: "channel", Reason: "mastermind has a channel", Round: pending.Round}, nil
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

// ConfirmAdmitted settles a pending mastermind entry a push route already
// admitted before the binding's state changed. It reads that route's queue
// back once and confirms on OutcomeDelivered; it NEVER pushes, so a done or
// paused binding cannot get a new payload after its state changed. An entry
// that was not admitted is left untouched.
//
// A done or paused binding's tick reaches delivery through this and only this
// call: without it an admitted entry stays pending forever, because an admitted
// entry is not claimable (ClaimableForMasterMind excludes it), so neither the
// background wait nor a reader can take it.
//
// An admit past the route's horizon is the one case that settles nothing: it is
// cleared and handed back as the pull route, which is what makes the entry
// claimable for the background wait. It never pushes -- a binding whose state
// already changed must not get a new payload.
func ConfirmAdmitted(ctx context.Context, d Deps, tx *store.Tx, b store.Binding) (store.Binding, Delivery, error) {
	pending, idx, found, err := tx.PendingForMasterMind(b.Name)
	if err != nil {
		return b, Delivery{}, err
	}
	if !found {
		return b, Delivery{Empty: true, Reason: "nothing pending"}, nil
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
		// The admit is cleared and the entry is left for the background wait.
		// This path NEVER pushes: a done or paused binding must not get a new
		// payload after its state changed, and clearing is what makes the entry
		// claimable again so the wait can take it.
		if err := expireAdmit(tx, b, pending, idx, age); err != nil {
			return b, Delivery{}, err
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

// deliverViaDeliverer runs one deliverer attempt for an entry the channel check
// did not take: Deliver when nobody has pushed the payload yet, Confirm when the
// push of this same tick admitted it, and ConfirmOnce -- one read-back, no poll
// -- when an earlier tick already admitted it. The read-back runs inside the
// caller's lock, so no reader can claim or print the entry while it is being
// confirmed: the admitting tick holds that lock for Confirm's full window, a
// repeat tick for a single read-back only.
//
// The admit is written BEFORE the read-back poll, so a crash during the poll
// leaves an admitted entry rather than a pending one, and the next tick reads
// the session back instead of sending again. The one exception is an admit past
// the route's horizon: it is cleared and the entry is pushed again, because a
// read-back that never finds the payload is not an answer and the admit would
// otherwise hold the queue forever.
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
		// The admit outlived the route's horizon and no read-back ever found the
		// payload, so the admit is no longer an answer. Clearing it falls through
		// to the Deliver below, and Deliver reads the session back before it
		// sends -- so a payload that did arrive is confirmed rather than posted
		// twice.
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

	// OutcomeNotMine and OutcomeUnavailable both leave the entry
	// pending: the deliverer's own reason is the answer, and the
	// background wait is the route that will finally take it.
	return b, Delivery{Route: "pull", Reason: reason, Round: pending.Round}, nil
}

// admitExpired reports whether an entry's admit has outlived the route's
// horizon, and how old it is. The clock is Deps.Now and the age is measured
// from the stamp the store wrote, not from the entry's TS.
//
// Without a bound, an entry whose payload never becomes visible to read-back
// (a deleted or compacted session, a table nothing covers, a time_created that
// does not match) stays admitted forever: the give-up gates all live inside
// Deliver, and Deliver is never called again for an admitted entry. The admit is
// the only thing keeping the entry out of the claimable scans, so it both blocks
// the binding's queue and hides the entry from wait, show and the cockpit.
//
// A route that reports no horizon keeps its admits unbounded, which is the
// pre-existing behaviour for a route that has not thought about the bound.
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

// expireAdmit clears an admit no read-back ever settled and says so once. The
// clearing is what makes the entry deliverable again, and because the stamp is
// gone afterwards this line is written at most once per expired admit.
func expireAdmit(tx *store.Tx, b store.Binding, pending store.LogEntry, idx int, age time.Duration) error {
	if err := tx.ClearAdmitIndex(b.Name, idx); err != nil {
		return err
	}
	slog.Info("admitted push was never read back; the admit is cleared and the payload is deliverable again",
		"binding", b.Name, "round", pending.Round, "age", age)
	return nil
}

// deliveryOf turns a deliverer outcome into the Delivery DeliverPending
// returns: only OutcomeDelivered confirms the entry, and anything else leaves
// it in the push route's own queue -- admitted, unconfirmed, and never
// presented to a reader.
func deliveryOf(tx *store.Tx, b store.Binding, idx int, route string, out Outcome, reason string, round int) (store.Binding, Delivery, error) {
	if out != OutcomeDelivered {
		return b, Delivery{Route: route, Reason: reason, Round: round}, nil
	}
	if err := tx.ConfirmIndex(b.Name, idx, route); err != nil {
		return b, Delivery{}, err
	}
	return b, Delivery{Delivered: true, Route: route, Reason: reason, Round: round}, nil
}

// mastermindLabel names the binding's mastermind for a delivery reason: the record's
// name when the registry knows it, else the mastermind id. Pure best-effort; a
// nil registry leaves the id.
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
