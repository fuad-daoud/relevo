package delivery

import (
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ackRoutePush is the route an ack writes on a confirmed entry: the one string
// both AckPush and the holder's awaitConfirm write, so a retry recognises its
// own work.
const ackRoutePush = "push"

// The sentinels AckPush refuses with. Each names one precondition the entry did
// not meet; the CLI classifies them into stable codes, so they are exported and
// documented rather than rendered as prose.
var (
	// ErrAckForeignBinding reports that the binding belongs to another
	// mastermind, so this ack would confirm an entry the caller may not see.
	ErrAckForeignBinding = errors.New("ack: binding belongs to another mastermind")
	// ErrAckUnknownSeq reports that no to-mastermind entry carries that seq.
	// A state line's seq 0 and every to-builder entry are covered by it.
	ErrAckUnknownSeq = errors.New("ack: no mastermind-bound entry with that seq")
	// ErrAckAlreadyConfirmed reports that the entry was already settled by a
	// different route, so an ack is not what settled it.
	ErrAckAlreadyConfirmed = errors.New("ack: entry already confirmed by another route")
	// ErrAckNotAdmitted reports that the entry is not (or no longer) admitted,
	// so no holder has written its line for this ack to confirm.
	ErrAckNotAdmitted = errors.New("ack: entry is not admitted")
	// ErrAckNoClaim reports that no live push claim holds the mastermind, so
	// there is no holder waiting for this ack.
	ErrAckNoClaim = errors.New("ack: no live push claim for this mastermind")
)

// AckPush confirms the binding's to-mastermind entry with seq, with route
// "push": the one-shot form of the ack the long-lived holder used to read from
// stdin. It resolves MasterMind ownership, the entry and its admit, and the
// holder's live claim inside ONE Store.WithLock, so an entry cannot be cleared
// or claimed by a reader between the check and the confirm.
//
// An entry already confirmed with route "push" is settled and returns nil, with
// no claim check: a retry whose first ack already landed must not be refused
// because the holder exited in between. Any other route is
// ErrAckAlreadyConfirmed.
func AckPush(d Deps, mastermindID, binding string, seq int) error {
	if d.Store == nil {
		return errors.New("ack: nil store")
	}
	if mastermindID == "" {
		return errors.New("ack: empty mastermind")
	}

	return d.Store.WithLock(func(tx *store.Tx) error {
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

// claimIsLive reports whether some holder still holds mastermindID's push
// claim. It answers under the caller's lock so the answer and the write that
// depends on it cannot be separated by a claim taking over.
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
