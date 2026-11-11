package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
)

// reopenPool closes the handle's pool and opens a fresh one on the same file,
// healing a pool whose WAL went stale under it: a truncate beside a live pool
// leaves the pool's index pointing at frames past the log's end, and every
// later statement through it fails. Only a quiescent pool is closed: shutting
// one down with a connection still checked out corrupts the engine's shared
// state for that path, so a busy pool is left alone and the caller fails
// loudly instead. The old pool must go before the fresh one opens -- a stale
// pool left open poisons the fresh one's probe -- so a failed probe reopens
// the original path as a best effort, keeping the handle serving instead of
// stranding it on the closed pool.
//
// An empty -wal holds no frames, so the -shm beside it indexes nothing: it is
// dropped so the fresh pool never reads the stale index. A -shm beside a
// non-empty WAL may index rows the fresh pool still needs, so it is left
// alone, and the log is never checkpointed here. Frames a truncate already
// destroyed are not resurrected, only reported by the statement that first
// missed them.
//
// The fresh pool answers a ping and a read probe before it replaces the old
// one, so a caller never swaps in a pool that is broken the same way; on any
// failure the statement fails with its own error instead of silently forever.
func (d *DB) reopenPool() (*sql.DB, error) {
	if d.path == "" {
		return nil, fmt.Errorf("db: reopen: not a direct handle: %w", ErrInvalid)
	}
	logSidecars(slog.Default(), "reopen-before", d.path)
	if n, err := walBytes(d.path); err == nil && n == 0 {
		// The index beside an empty log describes frames that are gone;
		// dropping it keeps the fresh pool from reading them.
		_ = os.Remove(d.path + "-shm")
	}
	old := d.sqlDB
	if old != nil {
		if old.Stats().InUse != 0 {
			return nil, fmt.Errorf("db: reopen %s: connections still out: %w", d.path, ErrBusy)
		}
		_ = old.Close()
	}
	pool, err := openPool(d.path, d.busy, false)
	if err == nil {
		err = ping(pool)
	}
	if err == nil {
		_, err = (&DB{sqlDB: pool}).Version()
	}
	if err != nil {
		if pool != nil {
			_ = pool.Close()
		}
		// Best effort: reopen the original path so the handle keeps serving
		// instead of stranding on the closed pool. The error below still
		// fails the statement loudly; it does not claim a recovery.
		if fallback, ferr := openPool(d.path, d.busy, false); ferr == nil {
			d.sqlDB = fallback
		}
		return nil, fmt.Errorf("db: reopen %s: probe: %w: %w", d.path, err, engineSentinel(err))
	}
	d.sqlDB = pool
	logSidecars(slog.Default(), "reopen-after", d.path)
	return pool, nil
}
