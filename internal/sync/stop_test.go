package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// restartLog is a log whose cancel releases nothing and refuses nothing: the
// shape a supervisor has once it restarts a cancelled worker for the next call.
// It counts the calls that reached it, so a test can tell a refusal the stop
// made from a call that went through.
type restartLog struct {
	*synclog.MemTransport
	entered   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
	once      sync.Once
	cancOnce  sync.Once
	calls     atomic.Int32
	cancels   atomic.Int32
}

func newRestartLog(origin string) *restartLog {
	return &restartLog{
		MemTransport: synclog.NewMemTransport(origin),
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
		cancelled:    make(chan struct{}),
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

func (r *restartLog) Cancel() {
	r.cancels.Add(1)
	r.cancOnce.Do(func() { close(r.cancelled) })
}

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

// TestStopTransportRefusesBatchBlobCallsAfterAStop pins the batch half of the
// stop's forward: a stopped export cannot upload or fetch bodies either, and
// nothing reaches the mover underneath to be stopped late.
func TestStopTransportRefusesBatchBlobCallsAfterAStop(t *testing.T) {
	t.Parallel()
	inner := blobLog{
		MemTransport: synclog.NewMemTransport("m1"),
		MemBlobMover: synclog.NewMemBlobMover(),
		staging:      t.TempDir(),
	}
	stop := NewStopTransport(inner)
	stop.Stop()

	items := []synclog.BlobBatchItem{{Key: "m1/deadbeef", Staging: filepath.Join(inner.staging, "deadbeef")}}
	if _, err := stop.PutBlobs(items); !errors.Is(err, ErrStopped) {
		t.Errorf("PutBlobs after a stop = %v, want ErrStopped", err)
	}
	if _, err := stop.GetBlobs(items); !errors.Is(err, ErrStopped) {
		t.Errorf("GetBlobs after a stop = %v, want ErrStopped", err)
	}
	if inner.Puts != 0 || inner.Gets != 0 {
		t.Errorf("the mover saw %d puts and %d gets after a stop, want neither", inner.Puts, inner.Gets)
	}
}

// TestStopTransportReportsAClientThatMovesNoBodies pins that a transport with
// no mover under it is a refusal the exporter can act on, on both sides of the
// batch: bodies cannot move, and a quiet success would append entries naming
// objects nobody stored.
func TestStopTransportReportsAClientThatMovesNoBodies(t *testing.T) {
	t.Parallel()
	stop := NewStopTransport(newRestartLog("m1"))
	items := []synclog.BlobBatchItem{{Key: "m1/deadbeef", Staging: "/nonexistent/deadbeef", Max: 64 << 20}}
	if _, err := stop.PutBlobs(items); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("PutBlobs with no mover = %v, want the invalid-client error", err)
	}
	if _, err := stop.GetBlobs(items); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("GetBlobs with no mover = %v, want the invalid-client error", err)
	}
}

// TestStopTransportForwardsBatchedBodies pins that the batch forward is real:
// a staged body reaches the mover through the wrapper and the fetched body
// comes back with its verdict, so the wrapper adds the stop rule and nothing
// else to a batch call.
func TestStopTransportForwardsBatchedBodies(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := []byte("a body moved through the stop wrapper")
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(staged, body, 0o600); err != nil {
		t.Fatalf("stage: %v", err)
	}
	inner := blobLog{
		MemTransport: synclog.NewMemTransport("m1"),
		MemBlobMover: synclog.NewMemBlobMover(),
		staging:      dir,
	}
	stop := NewStopTransport(inner)

	results, err := stop.PutBlobs([]synclog.BlobBatchItem{{Key: "m1/whatever", Staging: staged}})
	if err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil || results[0].Bytes != int64(len(body)) {
		t.Fatalf("PutBlobs verdicts = %+v, want the staged body moved", results)
	}
	out := filepath.Join(dir, "fetched")
	results, err = stop.GetBlobs([]synclog.BlobBatchItem{{Key: "m1/whatever", Staging: out, Max: int64(len(body))}})
	if err != nil {
		t.Fatalf("GetBlobs: %v", err)
	}
	if results[0].Err != nil || results[0].Bytes != int64(len(body)) {
		t.Fatalf("GetBlobs verdicts = %+v, want the body back", results)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != string(body) {
		t.Errorf("the forwarded fetch is not the body that went in (err %v)", err)
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
	expired := make(chan time.Time)
	done := make(chan error, 1)

	go func() {
		done <- withinUntil(context.Background(), stop, expired, func() error {
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

	close(expired)

	select {
	case <-log.cancelled:
	case <-time.After(10 * time.Second):
		t.Error("the stop never cancelled the worker")
	}

	close(log.release)

	select {
	case err := <-done:
		if !errors.Is(err, errStepTimedOut) {
			t.Errorf("within = %v, want errStepTimedOut", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the timed-out step did not end")
	}

	if got := log.calls.Load(); got != 1 {
		t.Errorf("%d calls reached the worker, want only the one in flight at the bound", got)
	}
}

// TestWithinStopsAStepBeforeItsFirstCall pins that an expired bound stops a
// step before its first call reaches the worker, refusing subsequent calls.
func TestWithinStopsAStepBeforeItsFirstCall(t *testing.T) {
	t.Parallel()
	log := newRestartLog("m1")
	stop := NewStopTransport(log)
	expired := make(chan time.Time)
	close(expired)
	done := make(chan error, 1)

	go func() {
		done <- withinUntil(context.Background(), stop, expired, func() error {
			select {
			case <-log.cancelled:
			case <-time.After(10 * time.Second):
				return errors.New("the stop never cancelled the worker")
			}
			_, err := stop.Append(nil)
			return err
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errStepTimedOut) {
			t.Errorf("within = %v, want errStepTimedOut", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the timed-out step did not end")
	}

	if got := log.calls.Load(); got != 0 {
		t.Errorf("%d calls reached the worker, want none", got)
	}
}

// TestStopTransportForwardsReadTraffic pins that a read through the wrapper
// reaches the log underneath while the work runs.
func TestStopTransportForwardsReadTraffic(t *testing.T) {
	t.Parallel()
	inner := synclog.NewMemTransport("m1")
	at := time.Now().UTC()
	if _, err := inner.PublishTraffic(synclog.TrafficRow{Month: "2026-10", Push: 7, Pull: 8, At: at}); err != nil {
		t.Fatalf("seed the traffic row: %v", err)
	}
	got, err := NewStopTransport(inner).ReadTraffic()
	if err != nil {
		t.Fatalf("ReadTraffic: %v", err)
	}
	if len(got) != 1 || got["m1"].Push != 7 || !got["m1"].At.Equal(at) {
		t.Errorf("ReadTraffic = %+v, want the row the log holds", got)
	}
}

// TestStopTransportRefusesReadTrafficAfterAStop pins that a stop ends the read
// with the stop rather than the log: a call starting after the flag cannot
// reach the fresh worker.
func TestStopTransportRefusesReadTrafficAfterAStop(t *testing.T) {
	t.Parallel()
	inner := synclog.NewMemTransport("m1")
	stop := NewStopTransport(inner)
	stop.Stop()
	if _, err := stop.ReadTraffic(); !errors.Is(err, ErrStopped) {
		t.Errorf("ReadTraffic after a stop = %v, want ErrStopped", err)
	}
}

// TestStopTransportWithoutTrafficReaderReportsUnsupported pins that a read over
// a transport without the reader is the sentinel, not a failure: the publisher
// is optional, and the guard reads it as no account figure rather than a join
// failure.
func TestStopTransportWithoutTrafficReaderReportsUnsupported(t *testing.T) {
	t.Parallel()
	stop := NewStopTransport(trafficBlind{synclog.NewMemTransport("m1")})
	if _, err := stop.ReadTraffic(); !errors.Is(err, errTrafficUnsupported) {
		t.Errorf("ReadTraffic over a transport without the reader = %v, want the unsupported sentinel", err)
	}
}
