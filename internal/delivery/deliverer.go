package delivery

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Outcome is what a MasterMindDeliverer reports back to DeliverPending.
type Outcome int

const (
	// OutcomeNotMine: this deliverer cannot address that mastermind; the pull
	// route takes the payload this same tick.
	OutcomeNotMine Outcome = iota
	// OutcomeUnavailable: the push path exists but is unreachable; next tick retries.
	OutcomeUnavailable
	// OutcomeAdmitted: the push path queued the payload but it is not read back
	// yet; the caller must record the admit and call Confirm, never Deliver again.
	OutcomeAdmitted
	// OutcomeDelivered: the session provably received the payload; only this confirms.
	OutcomeDelivered
)

// MasterMindDeliverer hands one payload to one mastermind over that harness's
// own push path, keyed by mastermind kind.
//
// Deliver must be idempotent: any non-delivered outcome retries next tick with
// the same payload. After OutcomeAdmitted the caller must call Confirm, never
// Deliver again.
//
// ref is the `relevo show …` command for the full text when the payload is too
// big to push, never a state-dir path. The reason must never contain a
// credential; the error is for a relevo bug, never an unreachable mastermind.
type MasterMindDeliverer interface {
	Deliver(ctx context.Context, mastermind store.Endpoint, payload, ref string, queuedAt time.Time) (Outcome, string, error)
	// Confirm reads the mastermind's own queue back and never sends: the only
	// call allowed for a payload Deliver admitted.
	Confirm(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error)
	// ConfirmOnce is the read-back for a repeat tick: one read-back, never a
	// poll or a send, so it holds the caller's lock briefly.
	ConfirmOnce(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error)
	// AdmitHorizon bounds how long an admitted entry may sit unconfirmed before
	// the admit expires and the entry is pending and claimable again. Zero
	// means admits never expire.
	AdmitHorizon() time.Duration
}
