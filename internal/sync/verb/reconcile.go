package verb

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// reconcile appends this file's difference from the log: upserts for missing or
// stale rows, deletes for vanished ones. It repairs writes the outbox cannot
// answer, holding only post-export writes. The dry run walks the same differences
// and appends nothing.
func (v *Runner) reconcile(ctx context.Context, verb *wire.SyncVerb) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	stop := relevosync.NewStopTransport(transport)
	defer v.Runner.Track(stop)()
	reconciler, err := keyedReconciler(v.Shared, stop)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	if verb.DryRun {
		preview, err := reconciler.Preview(relevosync.PreviewKeyLimit)
		if err != nil {
			return Refusal(Classify(err), err)
		}
		return &wire.SyncResult{OK: true, Report: relevosync.FormatPreview(preview)}
	}
	defer v.Runner.CountMoved(ctx, stop)
	res, err := reconciler.Reconcile()
	if err != nil {
		return Refusal(Classify(err), err)
	}
	return &wire.SyncResult{OK: true, Applied: res.Batches > 0, Report: relevosync.FormatReconcile(res)}
}
