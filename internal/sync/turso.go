//go:build !modernc

package sync

import (
	"context"
	"fmt"

	turso "turso.tech/database/tursogo"
)

// Turso drives a real synced database through SyncClient, so the driver's own
// signatures and the interface cannot drift apart without a build failure. No
// test constructs one: a test drives Fake, and this exists so the real client
// has a place to land that the interface already describes.
type Turso struct {
	db *turso.TursoSyncDb
}

// NewTurso wraps a synced database as a SyncClient.
func NewTurso(db *turso.TursoSyncDb) *Turso { return &Turso{db: db} }

var _ SyncClient = (*Turso)(nil)

// Push sends the local change set.
func (t *Turso) Push(ctx context.Context) error {
	if err := t.db.Push(ctx); err != nil {
		return fmt.Errorf("sync: push: %w", err)
	}
	return nil
}

// Pull fetches remote changes and rebases the local ones on top.
func (t *Turso) Pull(ctx context.Context) (bool, error) {
	applied, err := t.db.Pull(ctx)
	if err != nil {
		return false, fmt.Errorf("sync: pull: %w", err)
	}
	return applied, nil
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
