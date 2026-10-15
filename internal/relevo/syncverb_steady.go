package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The two halves of the steady exchange, each run alone for a client that asked
// for it. The daemon runs them together on its own triggers; these exist so
// `relevo db sync push` and `pull` are the same code rather than a second
// spelling of it.

// push drains this machine's outbox into the log: the export half of the steady
// exchange, run alone for a caller who asked for it. It appends only rows this
// installation owns, so an imported row never travels back.
func (v *VerbRunner) push(ctx context.Context) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	stop := relevosync.NewStopTransport(transport)
	defer v.Runner.Track(stop)()
	defer v.Runner.CountMoved(ctx, stop)
	res, err := exportOnce(v.Shared, stop)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	return &wire.SyncResult{OK: true, Applied: res.Appended > 0}
}

// pull reads every origin but this one and applies it: the import half of the
// steady exchange, run alone. A batch this machine cannot act on is dropped by
// the importer rather than failing the call, so one unreadable entry cannot
// stop every other origin.
func (v *VerbRunner) pull(ctx context.Context) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	stop := relevosync.NewStopTransport(transport)
	defer v.Runner.Track(stop)()
	defer v.Runner.CountMoved(ctx, stop)
	res, err := importOnce(v.Shared, stop)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	trouble := relevosync.TroubleFrom(res)
	relevosync.RecordTrouble(v.Local, trouble)
	return &wire.SyncResult{OK: true, Applied: res.Applied > 0, Holds: trouble.Holds()}
}
