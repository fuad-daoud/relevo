package sync

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// restartLog is a log whose cancel releases nothing and refuses nothing: the
// shape a supervisor has once it restarts a cancelled worker for the next call.
// It counts the calls that reached it, so a test can tell a refusal the stop
// made from a call that went through.
type restartLog struct {
	*synclog.MemTransport
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
	cancels atomic.Int32
}

func newRestartLog(origin string) *restartLog {
	return &restartLog{
		MemTransport: synclog.NewMemTransport(origin),
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
}

// Append is the call a test holds open: it waits for the test to release it,
// so the stop lands while the work sits between this call and the next.
func (r *restartLog) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.MemTransport.Append(entries)
}

func (r *restartLog) Pull(marks map[string]int) ([]synclog.Entry, error) {
	r.calls.Add(1)
	return r.MemTransport.Pull(marks)
}

func (r *restartLog) Head(origin string) ([]synclog.HeadRow, error) {
	r.calls.Add(1)
	return r.MemTransport.Head(origin)
}

func (r *restartLog) Stats() (synclog.Stats, error) {
	r.calls.Add(1)
	return r.MemTransport.Stats()
}

func (r *restartLog) Cancel() { r.cancels.Add(1) }

// TestStopTransportRefusesEveryCallAfterAStop pins the stop as sticky: the
// worker underneath is cancelled once, and no call after the stop reaches it,
// even though a restarted worker would answer.
func TestStopTransportRefusesEveryCallAfterAStop(t *testing.T) {
	t.Parallel()
	inner := newRestartLog("m1")
	close(inner.release)
	stop := NewStopTransport(inner)
	stop.Stop()

	if _, err := stop.Append(nil); !errors.Is(err, ErrStopped) {
		t.Errorf("Append after a stop = %v, want ErrStopped", err)
	}
	if _, err := stop.Pull(nil); !errors.Is(err, ErrStopped) {
		t.Errorf("Pull after a stop = %v, want ErrStopped", err)
	}
	if _, err := stop.Head("m1"); !errors.Is(err, ErrStopped) {
		t.Errorf("Head after a stop = %v, want ErrStopped", err)
	}
	if _, err := stop.Stats(); !errors.Is(err, ErrStopped) {
		t.Errorf("Stats after a stop = %v, want ErrStopped", err)
	}
	if got := inner.calls.Load(); got != 0 {
		t.Errorf("%d calls reached the worker after the stop, want none", got)
	}
	if got := inner.cancels.Load(); got != 1 {
		t.Errorf("the stop cancelled the worker %d times, want once", got)
	}
}

// TestRunnerStopEndsAnAttemptBetweenItsCalls pins what a disable relies on: a
// stop that lands while the attempt sits between two calls still ends it. The
// export's append is held open while the stop lands and then answers, as a
// call a cancel did not reach would; the attempt must end at its next call
// rather than go on to pull through a fresh worker.
func TestRunnerStopEndsAnAttemptBetweenItsCalls(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))

	log := newRestartLog("m1")
	runner := &Runner{Client: log, Local: local, Timeout: time.Minute}
	done := make(chan SteadyResult, 1)
	go func() { done <- runner.SyncOnce(context.Background(), shared) }()

	select {
	case <-log.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the attempt never reached its export")
	}
	runner.Stop()
	close(log.release)

	select {
	case res := <-done:
		if !errors.Is(res.Err, ErrStopped) {
			t.Errorf("SyncOnce = %v, want the attempt ended by the stop", res.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stopped attempt did not end")
	}
	if got := log.calls.Load(); got != 1 {
		t.Errorf("%d calls reached the worker, want only the append in flight at the stop", got)
	}
	if got := log.cancels.Load(); got != 1 {
		t.Errorf("the stop cancelled the worker %d times, want once", got)
	}
}

// TestRunnerStopWithNoAttemptIsANoOp pins that a disable on an idle machine has
// nothing to stop and stops nothing.
func TestRunnerStopWithNoAttemptIsANoOp(t *testing.T) {
	t.Parallel()
	log := newRestartLog("m1")
	(&Runner{Client: log}).Stop()
	var nilRunner *Runner
	nilRunner.Stop()
	if got := log.cancels.Load(); got != 0 {
		t.Errorf("an idle stop cancelled the worker %d times, want none", got)
	}
}

// TestWithinStopsTheStepAtItsBound pins the step bound as a stop rather than a
// worker cancel: a step whose first call outlives the bound, and is then
// answered as a restarted worker would answer it, must not reach the worker
// with its next call.
func TestWithinStopsTheStepAtItsBound(t *testing.T) {
	t.Parallel()
	log := newRestartLog("m1")
	stop := NewStopTransport(log)
	done := make(chan error, 1)
	go func() {
		done <- within(context.Background(), stop, 50*time.Millisecond, func() error {
			if _, err := stop.Append(nil); err != nil {
				return err
			}
			_, err := stop.Append(nil)
			return err
		})
	}()

	select {
	case <-log.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the step never reached its first call")
	}
	// Past the bound, so the stop has landed while the first call still waits.
	time.Sleep(300 * time.Millisecond)
	close(log.release)

	select {
	case err := <-done:
		if !errors.Is(err, errStepTimedOut) {
			t.Errorf("within = %v, want the step reported as timed out", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the timed-out step did not end")
	}
	if got := log.calls.Load(); got != 1 {
		t.Errorf("%d calls reached the worker, want only the one in flight at the bound", got)
	}
}
