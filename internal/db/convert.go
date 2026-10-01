//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// convertStepHook, when set, runs after each named conversion step and lets a
// test stop the conversion there, the way a kill would. A non-nil error aborts
// the conversion and is returned; the file stays in whatever state the step
// left it, which the next open's conversion must recover from. It is nil in
// production.
var convertStepHook func(step string) error

// The conversion's named steps, in order, for convertStepHook.
const (
	convertBackup     = "backup"
	convertCheckpoint = "checkpoint"
	convertRepair     = "repair"
	convertClosed     = "closed"
	convertSidecars   = "sidecars"
)

// convertLegacy turns a database an earlier, modernc build of relevo wrote into
// one Turso can open. It runs once and is safe if the process is killed at any
// step: every step is idempotent, and each leaves a consistent file on disk.
//
// The trigger is the -shm file. SQLite keeps one while a database is in WAL
// mode and deletes it on a clean last close; Turso never writes one. A -shm
// therefore means the file was last touched by SQLite, whose committed frames
// may still live only in the -wal -- which Turso must not open directly.
//
// The steps:
//
//   - back the file up to path+".pre-turso" through VACUUM INTO, unless a
//     backup already exists (a killed earlier attempt leaves one to reuse);
//   - open under modernc and drain the -wal with PRAGMA wal_checkpoint(TRUNCATE),
//     requiring the checkpoint not to be busy;
//   - repair every invalid UTF-8 TEXT value, which only modernc can read;
//   - close, so SQLite checkpoints and cleans its sidecars;
//   - refuse if the -wal still holds frames, and remove a leftover -shm.
func convertLegacy(path string) error {
	if _, err := os.Stat(path + "-shm"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("db: convert %s: stat -shm: %w", path, err)
	}

	pool, err := openConvertPool(path)
	if err != nil {
		return fmt.Errorf("db: convert %s: open modernc: %w: %w", path, ErrOpen, err)
	}
	// The pool is closed explicitly on every path except a hook abort: a hook
	// stands in for a kill, so the file must be left exactly as a kill would,
	// with SQLite's sidecars still on disk. A deferred close would clean them
	// up and make the abort indistinguishable from success.
	fail := func(cause error) error {
		_ = pool.Close()
		return cause
	}

	backup := path + ".pre-turso"
	switch _, serr := os.Stat(backup); {
	case errors.Is(serr, os.ErrNotExist):
		if err := vacuumInto(pool, backup); err != nil {
			return fail(fmt.Errorf("db: convert %s: backup: %w", path, err))
		}
	case serr != nil:
		return fail(fmt.Errorf("db: convert %s: stat backup: %w", path, serr))
	}
	if err := stepHook(convertBackup); err != nil {
		return err
	}

	ctx := context.Background()
	conn, err := pool.Conn(ctx)
	if err != nil {
		return fail(fmt.Errorf("db: convert %s: conn: %w", path, err))
	}
	busy, err := checkpointTruncate(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return fail(fmt.Errorf("db: convert %s: checkpoint: %w", path, err))
	}
	if busy != 0 {
		_ = conn.Close()
		return fail(fmt.Errorf("db: convert %s: the -wal is held by another connection: %w", path, ErrOpen))
	}
	if err := stepHook(convertCheckpoint); err != nil {
		_ = conn.Close()
		return err
	}

	if _, err := repairInvalidText(conn); err != nil {
		_ = conn.Close()
		return fail(fmt.Errorf("db: convert %s: repair text: %w", path, err))
	}
	if err := conn.Close(); err != nil {
		return fail(fmt.Errorf("db: convert %s: close conn: %w", path, err))
	}
	if err := stepHook(convertRepair); err != nil {
		return err
	}

	if err := pool.Close(); err != nil {
		return fmt.Errorf("db: convert %s: close: %w", path, err)
	}
	if err := stepHook(convertClosed); err != nil {
		return err
	}

	// A non-empty -wal after the clean close means some other connection still
	// holds frames; opening under Turso would lose them, so the conversion
	// refuses rather than risking it.
	if size, err := walSize(path); err != nil {
		return fmt.Errorf("db: convert %s: stat -wal: %w", path, err)
	} else if size > 0 {
		return fmt.Errorf("db: convert %s: the -wal still holds %d bytes: %w", path, size, ErrOpen)
	}
	// SQLite removes its own -shm on a clean last close; a leftover is the last
	// trace of the old mode and would make the next open convert again.
	if err := os.Remove(path + "-shm"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("db: convert %s: remove -shm: %w", path, err)
	}
	return stepHook(convertSidecars)
}

// stepHook runs the test hook when one is installed.
func stepHook(step string) error {
	if convertStepHook == nil {
		return nil
	}
	return convertStepHook(step)
}

// openConvertPool opens path under modernc, the only engine that can still read
// the file the old build wrote. The path is percent-encoded, so a `#`, `?` or
// `%` in it stays part of the filename.
func openConvertPool(path string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: path}).String() +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	return sql.Open("sqlite", dsn)
}

// checkpointTruncate runs PRAGMA wal_checkpoint(TRUNCATE) and returns its busy
// flag: zero when the whole -wal was checkpointed.
func checkpointTruncate(ctx context.Context, conn *sql.Conn) (int, error) {
	var busy, log, checked int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checked); err != nil {
		return 0, err
	}
	return busy, nil
}

// walSize is the size of path's -wal, or zero when it is absent.
func walSize(path string) (int64, error) {
	fi, err := os.Stat(path + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
