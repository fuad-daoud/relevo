package sync

// Stopping work that spans several transport calls. A worker cancel releases
// the one call in flight, but the supervisor starts a fresh worker for the next
// call, so an export, a reconcile or a join cancelled between two calls would
// carry on. A StopTransport makes the stop stick for the work it wraps: every
// call after the stop refuses at once, and the work ends at its next call.

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrStopped is what a call on a stopped transport returns. It names a stop a
// caller asked for, not a remote that failed.
var ErrStopped = errors.New("sync: the work was stopped")

// StopTransport is a transport one piece of work drives, which a stop ends for
// good. It never outlives that work: the client underneath is what the next
// piece of work drives, through a wrapper of its own.
type StopTransport struct {
	inner   synclog.LogTransport
	stopped atomic.Bool
}

var _ synclog.LogTransport = (*StopTransport)(nil)
var _ synclog.BlobTransport = (*StopTransport)(nil)

func NewStopTransport(inner synclog.LogTransport) *StopTransport {
	return &StopTransport{inner: inner}
}

// Stop ends the work: later calls refuse, and the worker is cancelled so a call
// in flight comes back now rather than at its bound. The flag goes first, so a
// call that starts after the cancel cannot reach the fresh worker.
func (s *StopTransport) Stop() {
	s.stopped.Store(true)
	cancelWorker(s.inner)
}

// Cancel is Stop under the name the worker-cancel callers look for.
func (s *StopTransport) Cancel() { s.Stop() }

// Inner is the transport underneath, for the caller that keeps the client once
// the work finished without a stop.
func (s *StopTransport) Inner() synclog.LogTransport { return s.inner }

func (s *StopTransport) Close() error {
	if c, ok := s.inner.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

func (s *StopTransport) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	if s.stopped.Load() {
		return nil, ErrStopped
	}
	return s.inner.Append(entries)
}

func (s *StopTransport) Pull(marks map[string]int) ([]synclog.Entry, error) {
	if s.stopped.Load() {
		return nil, ErrStopped
	}
	return s.inner.Pull(marks)
}

func (s *StopTransport) Head(origin string) ([]synclog.HeadRow, error) {
	if s.stopped.Load() {
		return nil, ErrStopped
	}
	return s.inner.Head(origin)
}

func (s *StopTransport) Stats() (synclog.Stats, error) {
	if s.stopped.Load() {
		return synclog.Stats{}, ErrStopped
	}
	return s.inner.Stats()
}

// The blob half, forwarded so a stopped export cannot upload a body the entry
// naming it was never appended for. BlobStagingDir reports an empty folder for a
// client that moves nothing, which is what tells the exporter there is no mover
// and every value should travel inline.
func (s *StopTransport) BlobStagingDir() string {
	if b, ok := s.inner.(interface{ BlobStagingDir() string }); ok {
		return b.BlobStagingDir()
	}
	return ""
}

func (s *StopTransport) PutBlob(key, stagingPath string) (int64, bool, error) {
	if s.stopped.Load() {
		return 0, false, ErrStopped
	}
	m, ok := s.inner.(synclog.BlobMover)
	if !ok {
		return 0, false, fmt.Errorf("sync: %w: the client moves no bodies", db.ErrInvalid)
	}
	return m.PutBlob(key, stagingPath)
}

func (s *StopTransport) GetBlob(key, stagingPath string) (int64, error) {
	if s.stopped.Load() {
		return 0, ErrStopped
	}
	m, ok := s.inner.(synclog.BlobMover)
	if !ok {
		return 0, fmt.Errorf("sync: %w: the client moves no bodies", db.ErrInvalid)
	}
	return m.GetBlob(key, stagingPath)
}
