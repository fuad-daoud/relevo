package relevo

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// reconcilePreview reports what a reconcile would append. Only the dry run is
// offered: enable and push already append, and a second route that does would
// be a second place for the guards to drift.
func (v *VerbRunner) reconcilePreview(ctx context.Context, verb *wire.SyncVerb) *wire.SyncResult {
	if !verb.DryRun {
		return verbRefusal(wire.SyncCodeInvalid,
			fmt.Errorf("sync: reconcile only previews; pass --dry-run: %w", db.ErrInvalid))
	}
	transport, err := v.drive(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	stop := relevosync.NewStopTransport(transport)
	defer v.Runner.Track(stop)()
	reconciler := synclog.NewReconciler(v.Shared, stop)
	relevosync.BindDocs(reconciler, v.Shared)
	preview, err := reconciler.Preview(relevosync.PreviewKeyLimit)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	return &wire.SyncResult{OK: true, Report: relevosync.FormatPreview(preview)}
}
