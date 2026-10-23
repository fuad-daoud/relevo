package relevo

// The corrupt-replica branch of a failed attempt.
//
// A replica that went unreadable on its own is the one failure a machine
// recovers from without a human, because rebuilding loses no data: the outbox
// is cleared only after the log confirmed the append, and the marks that say
// how far this machine has applied live in the machine-local file, not the
// replica, so a fresh replica re-reads the remote log and resumes exactly where
// the old one stopped. Every other failure -- a remote that refused, a network
// that timed out, a quota that ran out -- leaves the replica alone.

import (
	"context"
	"log/slog"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// rebuildCorruptReplica runs the rebuild policy on a corrupt-replica failure
// and logs the corrupt cause with the outcome.
//
// A rebuilt outcome queues one more attempt at once: queueSync marks the sync
// slot dirty, so the fresh bootstrap runs in this slot rather than at the next
// window. It cannot loop -- the record the rebuild wrote makes the next corrupt
// detection latch instead of rebuilding -- so at most two attempts are spent on
// one corruption. A latched outcome queues nothing: the breaker now stops every
// attempt until the retry verb clears the latch, and the latch's sentence
// already names that verb.
func (d *Daemon) rebuildCorruptReplica(ctx context.Context, runner *relevosync.Runner, cause error) {
	now := d.syncClock()()
	out, rebuilt, err := runner.RebuildCorrupt(now)
	switch {
	case err != nil:
		slog.Warn("sync: the replica is corrupt and the rebuild failed", "err", err, "cause", cause)
	case rebuilt:
		slog.Warn("sync: the replica is corrupt and was rebuilt", "cause", cause, "aside", out.Aside)
		d.queueSync(ctx)
	default:
		slog.Warn("sync: the replica is corrupt again inside the rebuild window", "cause", cause)
	}
}
