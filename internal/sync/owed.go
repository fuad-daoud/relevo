package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// A verb queues for the sync slot under its client's bound, DefaultTimeout, so what
// one attempt spends on a locked join's owed history has to end well inside that or
// every verb run during a long send would time out. A slice cannot interrupt a batch,
// and a batch of bodies takes far longer than one of rows, so both are bounded.
const (
	// DefaultOwedSlice is how long one attempt sends before it gives the slot back.
	DefaultOwedSlice = DefaultTimeout / 2
	// owedChunk is how many rows one batch carries.
	owedChunk = 64
)

// owedWalk is one run over this machine's file, with the transport it was started
// on. A walk never outlives that transport's client: a re-enable may have pointed
// the machine at another log, whose head this walk did not read.
type owedWalk struct {
	reconciler *synclog.Reconciler
	client     synclog.LogTransport
}

// sendOwed sends one slice of the history a locked join could not sign, and reports
// whether more is owed. The join marker is the only record of the debt, so a daemon
// restarted mid-send carries on from it: the new walk skips what head already holds.
// A failure drops the walk rather than resuming it, because a chunk that never
// reached the log has already moved the walk's place past its rows.
func (r *Runner) sendOwed(ctx context.Context, shared *db.DB, t *StopTransport) (bool, error) {
	progress, key, err := r.owedHistory()
	if err != nil || len(key) == 0 {
		r.owed = nil
		return false, err
	}
	walk := r.owedWalkOn(shared, t, key)
	var sent int
	var finished bool
	err = within(ctx, t, r.stepTimeout(), func() error {
		var sendErr error
		sent, finished, sendErr = sendOwedSlice(walk.reconciler, r.owedSlice())
		return sendErr
	})
	if err != nil {
		r.owed = nil
		return false, fmt.Errorf("sync: send this machine's history: %w", err)
	}
	if !finished {
		progress.Sent += sent
		return true, writeJoin(r.Local, progress)
	}
	r.owed = nil
	if err := r.publishOwedMarks(shared, t); err != nil {
		return false, err
	}
	return false, ClearJoin(r.Local)
}

// owedHistory reads the join marker and the key it can be sent under. No key means
// nothing can be sent now: no join is owed, or the machine is still locked.
func (r *Runner) owedHistory() (JoinProgress, []byte, error) {
	progress, joining, err := ReadJoin(r.Local)
	if err != nil || !joining {
		return JoinProgress{}, nil, err
	}
	key, err := SigningKey(r.Local)
	return progress, key, err
}

// owedWalkOn continues the walk in progress on this attempt's transport, or starts
// one when there is none for the client the machine now holds.
func (r *Runner) owedWalkOn(shared *db.DB, t *StopTransport, key []byte) *owedWalk {
	if r.owed != nil && r.owed.client == r.Client {
		r.owed.reconciler.Rebind(t)
		return r.owed
	}
	reconciler := synclog.NewReconciler(shared, t)
	reconciler.SetChunk(owedChunk)
	reconciler.SetKey(key)
	BindDocs(reconciler, shared)
	r.owed = &owedWalk{reconciler: reconciler, client: r.Client}
	return r.owed
}

// sendOwedSlice sends batches until the walk ends or the slice has passed, at least
// one each call so a very short slice still moves.
func sendOwedSlice(reconciler *synclog.Reconciler, slice time.Duration) (sent int, finished bool, _ error) {
	deadline := time.Now().Add(slice)
	for {
		one, err := reconciler.ReconcileBatch()
		if err != nil {
			return sent, false, err
		}
		if !one.More() {
			return sent, true, nil
		}
		sent += one.Upserts + one.Deletes
		if !time.Now().Before(deadline) {
			return sent, false, nil
		}
	}
}

// publishOwedMarks says where this machine has read to, as the end of a join does,
// without waiting for the steady throttle: a joined machine should be counted as a
// reader from the moment its join ends.
func (r *Runner) publishOwedMarks(shared *db.DB, t *StopTransport) error {
	marks, err := shared.ImportMarks()
	if err != nil {
		return fmt.Errorf("sync: read this machine's import marks: %w", err)
	}
	if err := t.PublishMarks(marks, time.Now().UTC()); err != nil {
		return fmt.Errorf("sync: publish this machine's import marks: %w", err)
	}
	return nil
}

func (r *Runner) owedSlice() time.Duration {
	if r.OwedSlice > 0 {
		return r.OwedSlice
	}
	return DefaultOwedSlice
}
