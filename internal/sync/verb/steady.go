package verb

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The two halves of the steady exchange, each runnable alone, so push and pull
// are the same code the daemon triggers together.

// push drains this machine's outbox into the log. It appends only owned rows, so
// an imported row never travels back.
func (v *Runner) push(ctx context.Context) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	stop := relevosync.NewStopTransport(transport)
	defer v.Runner.Track(stop)()
	defer v.Runner.CountMoved(ctx, stop)
	res, err := ExportOnce(v.Shared, stop)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	return &wire.SyncResult{OK: true, Applied: res.Appended > 0}
}

// pull reads every other origin and applies it. An unactionable batch is dropped,
// not failed, so one unreadable entry cannot stop every other origin.
func (v *Runner) pull(ctx context.Context) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	stop := relevosync.NewStopTransport(transport)
	defer v.Runner.Track(stop)()
	defer v.Runner.CountMoved(ctx, stop)
	res, err := ImportOnce(v.Shared, stop)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	trouble := relevosync.TroubleFrom(res)
	relevosync.RecordTrouble(v.Local, trouble)
	return &wire.SyncResult{OK: true, Applied: res.Applied > 0, Holds: trouble.Holds()}
}
