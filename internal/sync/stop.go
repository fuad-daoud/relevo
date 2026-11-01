package sync

// Stopping work that spans several transport calls: a worker cancel releases
// only the call in flight and the supervisor starts a fresh worker for the next
// call, so work cancelled between calls would carry on. A StopTransport makes
// the stop stick: every call after the stop refuses at once.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

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

func (s *StopTransport) AppendSigned(entries []synclog.Entry, boundary synclog.Boundary) ([]synclog.Entry, error) {
	if s.stopped.Load() {
		return nil, ErrStopped
	}
	if ba, ok := s.inner.(synclog.BoundaryAppender); ok {
		return ba.AppendSigned(entries, boundary)
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

func (s *StopTransport) PublishMarks(marks map[string]int, at time.Time) error {
	if s.stopped.Load() {
		return ErrStopped
	}
	return s.inner.PublishMarks(marks, at)
}

func (s *StopTransport) ReadMarks() (synclog.MarkState, error) {
	if s.stopped.Load() {
		return synclog.MarkState{}, ErrStopped
	}
	return s.inner.ReadMarks()
}

// errTrafficUnsupported is what PublishTraffic reports for a transport that
// carries no traffic publisher. It is a sentinel rather than a failure: the
// publisher is optional, and a client without it is skipped silently.
var errTrafficUnsupported = errors.New("sync: the client publishes no traffic")

// PublishTraffic writes this installation's traffic row and answers with every
// origin's row. The publisher is an optional interface on the transport
// underneath, so a client that carries none is reported with the sentinel
// rather than stopping the attempt.
func (s *StopTransport) PublishTraffic(row synclog.TrafficRow) (map[string]synclog.TrafficRow, error) {
	if s.stopped.Load() {
		return nil, ErrStopped
	}
	p, ok := s.inner.(synclog.TrafficPublisher)
	if !ok {
		return nil, errTrafficUnsupported
	}
	return p.PublishTraffic(row)
}

func (s *StopTransport) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	if s.stopped.Load() {
		return nil, ErrStopped
	}
	return s.inner.PullRange(origin, after, upto)
}

func (s *StopTransport) CompactOwn(floor int) (synclog.CompactResult, error) {
	if s.stopped.Load() {
		return synclog.CompactResult{}, ErrStopped
	}
	return s.inner.CompactOwn(floor)
}

func (s *StopTransport) KeyMeta(ctx context.Context) (synclog.KeyMeta, error) {
	if s.stopped.Load() {
		return synclog.KeyMeta{}, ErrStopped
	}
	return s.inner.KeyMeta(ctx)
}

func (s *StopTransport) KeyInit(ctx context.Context, key synclog.KeyMeta) (synclog.KeyMeta, error) {
	if s.stopped.Load() {
		return synclog.KeyMeta{}, ErrStopped
	}
	return s.inner.KeyInit(ctx, key)
}

func (s *StopTransport) KeyRekey(ctx context.Context, spec synclog.KeyRekeySpec) (synclog.KeyMeta, error) {
	if s.stopped.Load() {
		return synclog.KeyMeta{}, ErrStopped
	}
	return s.inner.KeyRekey(ctx, spec)
}

func (s *StopTransport) PublishEpochCut(ctx context.Context, cut synclog.EpochCut) error {
	if s.stopped.Load() {
		return ErrStopped
	}
	return s.inner.PublishEpochCut(ctx, cut)
}

func (s *StopTransport) ClaimIdentity(ctx context.Context, origin, token string, at time.Time) (string, time.Time, error) {
	if s.stopped.Load() {
		return "", time.Time{}, ErrStopped
	}
	return s.inner.ClaimIdentity(ctx, origin, token, at)
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

func (s *StopTransport) GetBlob(key, stagingPath string, max int64) (int64, error) {
	if s.stopped.Load() {
		return 0, ErrStopped
	}
	m, ok := s.inner.(synclog.BlobMover)
	if !ok {
		return 0, fmt.Errorf("sync: %w: the client moves no bodies", db.ErrInvalid)
	}
	return m.GetBlob(key, stagingPath, max)
}
