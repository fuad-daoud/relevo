package delivery

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Outcome is what a MasterMindDeliverer reports back to DeliverPending.
type Outcome int

const (
	// OutcomeNotMine: this deliverer cannot address that mastermind -- wrong kind,
	// no usable session id, a missing dependency, or it has given up
	// (FallbackAfter). The pane path takes the payload this same tick.
	OutcomeNotMine Outcome = iota
	// OutcomeUnavailable: the push path exists but is not reachable right now.
	// The payload stays pending; the next tick retries.
	OutcomeUnavailable
	// OutcomeAdmitted: the push path accepted the payload into its own queue
	// (a 2xx from opencode's API, agy's "sent"), but the session has not been
	// read back yet. The caller must record the admit and then call Confirm;
	// it must never call Deliver for that payload again.
	OutcomeAdmitted
	// OutcomeDelivered: the mastermind's session provably received the payload.
	// Only this value may confirm the entry.
	OutcomeDelivered
)

// MasterMindDeliverer hands one payload to one mastermind over that harness's
// own push path. Implementations are keyed by mastermind kind and are called
// from the daemon's tick goroutine.
//
// Deliver must be idempotent in effect: after any non-OutcomeDelivered outcome it
// is called again next tick with the same payload, so it must not deliver
// twice. After OutcomeAdmitted the caller must call Confirm, never Deliver
// again: the payload is already in that route's queue, and only the read-back
// may admit it.
//
// ref is how the mastermind reads the full text when the payload is too big to
// push: the `relevo show …` command that prints the entry, never a state-dir
// path.
//
// The returned string is a short reason for the log and Delivery.Reason,
// and must never contain a credential. The error is for a bug in relevo
// (a request it could not build); an unreachable mastermind is OutcomeUnavailable,
// not an error.
type MasterMindDeliverer interface {
	Deliver(ctx context.Context, mastermind store.Endpoint, payload, ref string, queuedAt time.Time) (Outcome, string, error)
	// Confirm reads the mastermind's own queue back and never sends: it is the
	// only call allowed for a payload Deliver admitted.
	Confirm(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error)
	// ConfirmOnce is the read-back for a tick that found the payload already
	// admitted: exactly one read-back, never a poll and never a send. The tick
	// that admitted the payload keeps Confirm's full window; every later tick
	// calls ConfirmOnce, so a repeat tick holds the caller's lock for one
	// read-back instead of the whole window.
	ConfirmOnce(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error)
	// AdmitHorizon bounds how long an entry this route admitted may sit
	// unconfirmed before the admit is treated as expired: the stamp is cleared
	// and the entry becomes pending and claimable again. It is the same give-up
	// horizon Deliver already defends, reached through one accessor so the
	// caller need not know the route. Zero means the route sets no bound and its
	// admits never expire.
	AdmitHorizon() time.Duration
}
