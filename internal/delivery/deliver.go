package delivery

import (
	"context"
	"fmt"

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
		text, _ := PushText(pending, b, d.Store.ReadFile)
		out, reason, err := del.Deliver(ctx, b.MasterMind, text, LogRef(b, pending), pending.TS)
		if err != nil {
			return b, Delivery{}, fmt.Errorf("deliver to mastermind: %w", err)
		}
		if out == OutcomeDelivered {
			route := "deliverer:" + kind
			if err := tx.ConfirmIndex(b.Name, idx, route); err != nil {
				return b, Delivery{}, err
			}
			return b, Delivery{Delivered: true, Route: route, Reason: reason, Round: pending.Round}, nil
		}
		// OutcomeNotMine and OutcomeUnavailable both leave the entry
		// pending: the deliverer's own reason is the answer, and the
		// background wait is the route that will finally take it.
		return b, Delivery{Route: "pull", Reason: reason, Round: pending.Round}, nil
	}

	return b, Delivery{
		Route:  "pull",
		Reason: fmt.Sprintf("awaiting pull for mastermind %s (%s)", mastermindLabel(d, b), kind),
		Round:  pending.Round,
	}, nil
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
