package delivery

import (
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ackRoutePush is the route an ack confirms with, so a retry recognises its own work.
const ackRoutePush = "push"

// The sentinels AckPush refuses with, one per unmet precondition. The CLI maps
// them to stable codes, so they stay exported and documented.
var (
	// ErrAckForeignBinding reports the binding belongs to another mastermind.
	ErrAckForeignBinding = errors.New("ack: binding belongs to another mastermind")
	// ErrAckUnknownSeq reports no mastermind-bound entry carries that seq.
	ErrAckUnknownSeq = errors.New("ack: no mastermind-bound entry with that seq")
	// ErrAckAlreadyConfirmed reports another route already settled the entry.
	ErrAckAlreadyConfirmed = errors.New("ack: entry already confirmed by another route")
	// ErrAckNotAdmitted reports the entry is not admitted, so no line exists to confirm.
	ErrAckNotAdmitted = errors.New("ack: entry is not admitted")
	// ErrAckNoClaim reports no live push claim holds the mastermind.
	ErrAckNoClaim = errors.New("ack: no live push claim for this mastermind")
)

// AckResult is what an ack settled: AlreadyConfirmed is true when the call
// recognised its own route's earlier work instead of writing fresh.
type AckResult struct {
	Binding          string
	Seq              int
	Route            string
	AlreadyConfirmed bool
}

// AckPush confirms the binding's mastermind-bound entry with seq, with route
// "push": ownership, entry, admit and the holder's live claim resolve inside
// one Store.WithLock, so no reader slips between check and confirm. An entry
// already confirmed with route "push" returns AlreadyConfirmed with no claim
// check, so a retried ack survives the holder exiting in between.
func AckPush(d Deps, mastermindID, binding string, seq int) (AckResult, error) {
	if d.Store == nil {
		return AckResult{}, errors.New("ack: nil store")
	}
	if mastermindID == "" {
		return AckResult{}, errors.New("ack: empty mastermind")
	}

	res := AckResult{Binding: binding, Seq: seq, Route: ackRoutePush}
	return res, d.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(binding)
		if err != nil {
			return err
		}
		if b.MasterMindID != mastermindID {
			return fmt.Errorf("ack %q seq %d: %w", binding, seq, ErrAckForeignBinding)
		}

		entries, err := tx.ReadLog(binding)
		if err != nil {
			return err
		}
		idx := -1
		for i, e := range entries {
			if e.Seq == seq && e.Direction == store.DirToMasterMind {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("ack %q seq %d: %w", binding, seq, ErrAckUnknownSeq)
		}

		e := entries[idx]
		if e.Confirmed {
			if e.Route == ackRoutePush {
				res.AlreadyConfirmed = true
				return nil
			}
			return fmt.Errorf("ack %q seq %d: %w (route %q)", binding, seq, ErrAckAlreadyConfirmed, e.Route)
		}
		if e.AdmittedAt == nil {
			return fmt.Errorf("ack %q seq %d: %w", binding, seq, ErrAckNotAdmitted)
		}
		if !claimIsLive(d, mastermindID) {
			return fmt.Errorf("ack %q seq %d: %w", binding, seq, ErrAckNoClaim)
		}
		return tx.ConfirmIndex(binding, idx, ackRoutePush)
	})
}

// claimIsLive answers under the caller's lock, so the answer and the write
// depending on it cannot be split by a claim taking over.
func claimIsLive(d Deps, mastermindID string) bool {
	if d.Channels == nil {
		return false
	}
	var now time.Time
	if d.Now != nil {
		now = d.Now()
	}
	c, err := d.Channels.Live(mastermindID, now)
	return err == nil && c != nil
}
