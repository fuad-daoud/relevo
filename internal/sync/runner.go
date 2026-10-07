package sync

// The runner is the seam a remote would be driven through. This build carries no
// sync engine, so nothing builds a client and nothing drives one: what is left
// is the shape the daemon and the cockpit install, the bound one attempt would
// run under, and the marker keys the statusline reads back. They stay so the
// wiring has one place to come back to rather than a new one beside it.

import (
	"errors"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrAuthRefused is the one failure a retry cannot fix: the remote rejected this
// installation's token, so every later attempt is refused the same way until a
// new token is minted. It is the whole difference between a machine that is
// behind and a machine a human has to unblock.
var ErrAuthRefused = errors.New("sync: the remote refused this installation's token")

// DefaultTimeout bounds one push-then-pull. The bound is load-bearing: it is
// what keeps a blackholed network off the seal path and out of a tick, so a
// remote that never answers costs one bounded wait rather than a hung round.
const DefaultTimeout = 20 * time.Second

// KeyStats is the snapshot of what the remote last reported, kept where the sync
// view will read it. The statusline never reads this key: it reads one marker at
// a time and formats a token.
const KeyStats = "sync.stats"

// Runner is the remote seam, and nothing else: there is no engine in this build
// to drive through it, so no field here is written by a tick. A caller installs
// one because the wiring around it is installed unconditionally, and a machine
// that once synced must still be able to be told what it is set to be.
type Runner struct {
	// Client is the remote seam. Nil means sync is off, and nothing reads it
	// while it is.
	Client SyncClient
	// Local is the machine-local kv the markers are written through, so a
	// marker can never reach the file that leaves this machine.
	Local db.KV
}

// Enabled reports whether there is anything to drive. A machine with no client
// or no local file has no sync, and a trigger skips it entirely rather than
// queueing work that can only fail.
func (r *Runner) Enabled() bool {
	return r != nil && r.Client != nil && r.Local != nil
}
