package sync

// The runner is the seam a remote is driven through. It holds the log transport
// a pipe client provides and the machine-local kv the markers are written
// through, and it carries the bound one attempt runs under. The client is built
// where the wiring is and installed here, so every daemon path hands one holder
// around rather than building a client each.

import (
	"errors"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
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

// Runner is the remote seam. It holds the log transport a pipe client provides,
// so a transport method cannot be lost without the tree failing to build.
type Runner struct {
	// Client is the transport the log is exchanged over. Nil means no client is
	// open, and nothing reads it while it is.
	Client synclog.LogTransport
	// Local is the machine-local kv the markers are written through, so a
	// marker can never reach the file that leaves this machine.
	Local db.KV
	// Timeout bounds one step of the steady pipeline. Zero means StepTimeout,
	// which is the bound production runs under; a test shortens it so a step
	// that overruns is reached without waiting the real bound out.
	Timeout time.Duration
	// Blobs returns the in-process bucket store, or nil when the machine has no
	// bucket. It is a func so the credentials are read when needed.
	Blobs func() blobstore.BlobStore
	// ReplicaPath is the worker's replica file, whose size is the Turso storage
	// in use.
	ReplicaPath string

	// active is the transport the work in flight drives, so Stop can end it
	// from outside the sync slot that work holds.
	active atomic.Pointer[StopTransport]

	// lastRead is the worker's cumulative totals as of the last attempt, which
	// the next reading is measured against.
	readMu   gosync.Mutex
	lastRead synclog.Stats

	// roundsOpen is the daemon's last noted open-round count, stored as n+1
	// with 0 meaning unknown: the note arrives on the daemon tick while
	// attempts read it on the sync goroutine.
	roundsOpen atomic.Int64
}

// Enabled reports whether there is anything to drive. A machine with no client
// or no local file has no sync, and a trigger skips it entirely rather than
// queueing work that can only fail.
func (r *Runner) Enabled() bool {
	return r != nil && r.Client != nil && r.Local != nil
}
