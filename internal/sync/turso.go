//go:build !modernc

package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	turso "turso.tech/database/tursogo"
)

// Turso drives a real synced database through SyncClient, so the driver's own
// signatures and the interface cannot drift apart without a build failure. No
// test constructs one: a test drives Fake, and this exists so the real client
// has a place to land that the interface already describes.
//
// It also carries the open that built it, so a pull that has to undo a stale
// watermark can rebuild the handle rather than retry through one that has
// already read the sidecar it is about to change.
type Turso struct {
	db   syncDatabase
	path string
	// Reopen builds a fresh handle over the same remote, for the retry a stale
	// watermark needs. Nil means there is nothing to rebuild with, which is the
	// case for a handle a caller handed in directly; the pull then reports the
	// original refusal rather than pretending a retry happened.
	Reopen func(context.Context) (syncDatabase, error)
	mu     sync.Mutex
}

// syncDatabase is the driver's synced database as this package drives it. It is
// an interface rather than the driver's own type so a test can stand in for the
// engine's refusals, which are only reachable against a real remote.
type syncDatabase interface {
	Push(context.Context) error
	Pull(context.Context) (bool, error)
	Stats(context.Context) (turso.TursoSyncDbStats, error)
	Checkpoint(context.Context) error
}

var _ syncDatabase = (*turso.TursoSyncDb)(nil)

// NewTurso wraps a synced database as a SyncClient. A handle built this way
// cannot invalidate a stale watermark, because nothing here names the file.
func NewTurso(db *turso.TursoSyncDb) *Turso { return &Turso{db: db} }

var _ SyncClient = (*Turso)(nil)

// Push sends the local change set.
//
// A refusal the remote answers with is classed here, once, so every caller above
// this one sees a class it can branch on rather than the driver's prose. The
// message names the call and the reason; nothing the remote chose reaches it.
func (t *Turso) Push(ctx context.Context) error {
	if err := t.db.Push(ctx); err != nil {
		return classifyRemoteRefusal("push", fmt.Errorf("sync: push: %w", err))
	}
	return nil
}

// Pull fetches remote changes and rebases the local ones on top.
//
// A checkpoint the engine's persisted watermark can never satisfy is undone here
// and retried once. The watermark names a frame in the local log, so a watermark
// above the log's highest frame names a frame that does not exist; left in place
// it refuses every future pull too, over a file whose rows are fine. Clearing it
// and retrying is what makes such a machine sync again, and the retry is once:
// a refusal that survives the invalidation is a different fault and is reported
// rather than looped on.
//
// The retry needs a handle that has not already read the sidecar, so the open is
// rebuilt rather than reused. That is why the handle keeps the config it was
// built from.
func (t *Turso) Pull(ctx context.Context) (bool, error) {
	applied, err := t.db.Pull(ctx)
	if err == nil {
		return applied, nil
	}
	// The refusal classes are read before the watermark is: a remote that
	// refused a statement is not a stale watermark, and the invalidation must not
	// spend a rebuild on one.
	err = classifyRemoteRefusal("pull", err)
	if errors.Is(err, ErrRemoteRefused) || errors.Is(err, ErrRemoteSchema) {
		return false, err
	}
	retry, rerr := t.invalidateAndReopen(ctx, err)
	if rerr != nil {
		return false, rerr
	}
	if !retry {
		return false, fmt.Errorf("sync: pull: %w", err)
	}
	applied, err = t.db.Pull(ctx)
	if err != nil {
		return false, fmt.Errorf("sync: pull: still failing after the stale revert watermark was cleared: %w", err)
	}
	return applied, nil
}

// invalidateAndReopen decides whether err is a watermark the log can never
// reach, and if it is, clears that watermark and rebuilds the handle so the
// retry reads the sidecar as it now is.
//
// Every other refusal is the caller's to report unchanged, and so is a watermark
// the log can still reach: that one a checkpoint can satisfy, and clearing it
// would ask the engine to replay frames it has already taken.
func (t *Turso) invalidateAndReopen(ctx context.Context, cause error) (bool, error) {
	refused, isWatermark := RefusedWatermark(classifyWatermarkRefusal(cause))
	if !isWatermark || t.path == "" || t.Reopen == nil {
		return false, nil
	}
	maxFrame, err := WALMaxFrame(t.path)
	if err != nil {
		return false, fmt.Errorf("sync: pull: %w: %w", err, cause)
	}
	cleared, err := InvalidateStaleWatermark(t.path, maxFrame)
	switch {
	case errors.Is(err, ErrNoWatermark):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("sync: pull: %w: %w", err, cause)
	case !cleared:
		return false, nil
	}
	slog.Warn("sync: the persisted revert watermark is above this log's highest frame, so no checkpoint could satisfy it; it was cleared and the pull retried once",
		"path", t.path, "watermark", refused, "wal_max_frame", maxFrame)

	t.mu.Lock()
	defer t.mu.Unlock()
	// The driver exposes no way to close a handle, so the old one is dropped
	// rather than closed. Nothing drives it again, so it never writes the
	// sidecar back over the one just rewritten; what it still holds is the file
	// descriptor the engine opened, until this process ends.
	t.db = nil
	fresh, err := t.Reopen(ctx)
	if err != nil {
		return false, fmt.Errorf("sync: pull: %w: reopen after clearing the stale watermark: %w", cause, err)
	}
	t.db = fresh
	return true, nil
}

// Stats reports the local change set, carrying the driver's numbers over
// unchanged so a view can render them without knowing the driver's shape.
func (t *Turso) Stats(ctx context.Context) (Stats, error) {
	stats, err := t.db.Stats(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("sync: stats: %w", err)
	}
	return Stats{
		CdcOperations:        stats.CdcOperations,
		LastPullUnixTime:     stats.LastPullUnixTime,
		LastPushUnixTime:     stats.LastPushUnixTime,
		NetworkSentBytes:     stats.NetworkSentBytes,
		NetworkReceivedBytes: stats.NetworkReceivedBytes,
		Revision:             stats.Revision,
	}, nil
}

// Checkpoint truncates the local write-ahead log.
func (t *Turso) Checkpoint(ctx context.Context) error {
	if err := t.db.Checkpoint(ctx); err != nil {
		return fmt.Errorf("sync: checkpoint: %w", err)
	}
	return nil
}
