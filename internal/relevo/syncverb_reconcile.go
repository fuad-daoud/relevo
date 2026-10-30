package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	syncverb "github.com/fuad-daoud/relevo/internal/sync/verb"
)

// reconcile compares this machine's file against what the log holds for its
// origin and appends the difference: an upsert for every row head lacks or
// holds stale, and a delete for every head row the file no longer holds. It is
// the repair for a log that missed this machine's writes, which the outbox
// cannot answer because it holds only what was written since the last export.
// The dry run walks the same differences and appends nothing.
func (v *VerbRunner) reconcile(ctx context.Context, verb *wire.SyncVerb) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return syncverb.Refusal(syncverb.Classify(err), err)
	}
	stop := relevosync.NewStopTransport(transport)
	defer v.Runner.Track(stop)()
	reconciler, err := keyedReconciler(v.Shared, stop)
	if err != nil {
		return syncverb.Refusal(syncverb.Classify(err), err)
	}
	if verb.DryRun {
		preview, err := reconciler.Preview(relevosync.PreviewKeyLimit)
		if err != nil {
			return syncverb.Refusal(syncverb.Classify(err), err)
		}
		return &wire.SyncResult{OK: true, Report: relevosync.FormatPreview(preview)}
	}
	defer v.Runner.CountMoved(ctx, stop)
	res, err := reconciler.Reconcile()
	if err != nil {
		return syncverb.Refusal(syncverb.Classify(err), err)
	}
	return &wire.SyncResult{OK: true, Applied: res.Batches > 0, Report: relevosync.FormatReconcile(res)}
}
