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
// twice.
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
}
