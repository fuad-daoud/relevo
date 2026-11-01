package syncworker

import (
	"context"
	"database/sql"
	"fmt"

	turso "turso.tech/database/tursogo"
)

// The two transfer bounds: a bootstrap download is fetched in threshold-sized
// pieces, and a push is split on transaction boundaries once it has grown that
// large, so one driver call never moves an unbounded amount over the wire. A
// driver bump revisits them deliberately rather than inheriting the new default.
const (
	pushOperationsThreshold = 4096
	pullBytesThreshold      = 4 << 20
)

// syncDb is the driver's own handle on the replica, named as an interface so
// the seam above it can be driven by a double.
type syncDb interface {
	Connect(ctx context.Context) (*sql.DB, error)
	Push(ctx context.Context) error
	Pull(ctx context.Context) (bool, error)
	Stats(ctx context.Context) (turso.TursoSyncDbStats, error)
	Checkpoint(ctx context.Context) error
}

// newSyncDb is the sync constructor as a variable so a test can stand in for
// it: the real one reaches the network, and no CI test may.
var newSyncDb = func(ctx context.Context, cfg turso.TursoSyncDbConfig) (syncDb, error) {
	return turso.NewTursoSyncDb(ctx, cfg)
}

// tursoDriver is the replica driver production uses. It opens relevo-sync.db
// only through the sync constructor and hands every statement back through
// Connect(), which keeps the engine's view of the file from being invalidated
// by a second opener.
type tursoDriver struct {
	path string
	sdb  syncDb
}

// newTursoDriver returns the driver for one replica path, held here and nowhere
// else: the backend above never names a file.
func newTursoDriver(path string) *tursoDriver {
	return &tursoDriver{path: path}
}

// Open bootstraps the replica from the remote and connects to it.
// BootstrapIfEmpty is true so an empty remote yields an empty replica this
// worker then teaches the log's schema.
func (d *tursoDriver) Open(ctx context.Context, spec Spec) (*sql.DB, error) {
	if d.sdb != nil {
		return nil, fmt.Errorf("syncworker: replica %s is already open", d.path)
	}
	bootstrap := true
	sdb, err := newSyncDb(ctx, turso.TursoSyncDbConfig{
		Path:                    d.path,
		RemoteUrl:               spec.URL,
		AuthToken:               spec.Token,
		BootstrapIfEmpty:        &bootstrap,
		PushOperationsThreshold: pushOperationsThreshold,
		PullBytesThreshold:      pullBytesThreshold,
	})
	if err != nil {
		return nil, fmt.Errorf("syncworker: bootstrap %s: %w", d.path, err)
	}
	d.sdb = sdb
	db, err := sdb.Connect(ctx)
	if err != nil {
		d.sdb = nil
		return nil, fmt.Errorf("syncworker: connect to %s: %w", d.path, err)
	}
	return db, nil
}

// Push sends the replica's local changes to the remote.
func (d *tursoDriver) Push(ctx context.Context) error {
	return d.sdb.Push(ctx)
}

// Pull applies the remote's changes to the replica.
func (d *tursoDriver) Pull(ctx context.Context) error {
	_, err := d.sdb.Pull(ctx)
	return err
}

// Stats is the engine's own health call, and the network counters come back
// with it: they are what the byte totals are built from.
func (d *tursoDriver) Stats(ctx context.Context) (turso.TursoSyncDbStats, error) {
	return d.sdb.Stats(ctx)
}

// Checkpoint folds the replica's WAL into its database file.
func (d *tursoDriver) Checkpoint(ctx context.Context) error {
	return d.sdb.Checkpoint(ctx)
}

// bytesDelta returns what one push or pull moved, from the engine's own totals
// before and after it. The counters reset when the engine re-creates them
// after a long disconnect or a re-bootstrap, so a negative difference reads as
// a reset and counts `after` alone. A zero difference is a transfer that moved
// nothing: an empty outbox and a pull with no remote changes both report it.
func bytesDelta(before, after turso.TursoSyncDbStats) (sent, recv int64) {
	sent, recv = after.NetworkSentBytes-before.NetworkSentBytes,
		after.NetworkReceivedBytes-before.NetworkReceivedBytes
	if sent < 0 {
		sent = after.NetworkSentBytes
	}
	if recv < 0 {
		recv = after.NetworkReceivedBytes
	}
	return sent, recv
}
