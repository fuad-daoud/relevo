package relevo

import (
	"context"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
)

func (d *Daemon) rebuildCorruptReplica(ctx context.Context, runner *relevosync.Runner, cause error) {
	verb.RebuildCorruptReplica(ctx, runner, cause, syncEngine{d})
}
