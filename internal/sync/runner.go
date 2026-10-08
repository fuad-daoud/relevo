package sync

// The runner is the seam a remote is driven through. It holds the log transport
// a pipe client provides and the machine-local kv the markers are written
// through, and it carries the bound one attempt would run under and the marker
// keys the statusline reads back. The client is built where the wiring is built
// and installed here, so the daemon and the cockpit hand around one holder
// rather than build a client each.

import (
	"errors"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
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

// Runner is the remote seam. It holds the log transport a pipe client provides,
// so a transport method cannot be lost without the tree failing to build, and a
// caller installs one because the wiring around it is installed unconditionally.
type Runner struct {
	// Client is the transport the log is exchanged over. Nil means no client is
	// open, and nothing reads it while it is.
	Client synclog.LogTransport
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
