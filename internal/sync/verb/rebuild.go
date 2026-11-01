package verb

import (
	"context"
	"log/slog"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// RebuildCorruptReplica runs the rebuild policy on a corrupt-replica failure,
// logging cause with outcome. Rebuilt queues one more attempt through the engine;
// it cannot loop, since the rebuild's record latches the next detection. Latched
// queues nothing: the breaker stops attempts until the retry verb clears it.
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
