package verb

import (
	"context"
	"log/slog"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// RebuildCorruptReplica runs the rebuild policy on a corrupt-replica failure
// and logs the corrupt cause with the outcome.
//
// A rebuilt outcome queues one more attempt at once through the engine, so the
// fresh bootstrap runs in this slot rather than at the next window. It cannot
// loop -- the record the rebuild wrote makes the next corrupt detection latch
// instead of rebuilding -- so at most two attempts are spent on one corruption.
// A latched outcome queues nothing: the breaker now stops every attempt until
// the retry verb clears the latch, and the latch's sentence already names that
// verb.
func RebuildCorruptReplica(ctx context.Context, runner *relevosync.Runner, cause error, eng Engine) {
	now := eng.Clock()()
	out, rebuilt, err := runner.RebuildCorrupt(now)
	switch {
	case err != nil:
		slog.Warn("sync: the replica is corrupt and the rebuild failed", "err", err, "cause", cause)
	case rebuilt:
		slog.Warn("sync: the replica is corrupt and was rebuilt", "cause", cause, "aside", out.Aside)
		eng.QueueSync(ctx)
	default:
		slog.Warn("sync: the replica is corrupt again inside the rebuild window", "cause", cause)
	}
}
