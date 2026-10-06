package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mkdirVacuumTemp creates the backup's temp directory. It is a var so a test can
// observe the directory's mode before the deferred remove takes it away.
var mkdirVacuumTemp = os.MkdirTemp

// BackupTo copies the whole database to path with VACUUM INTO. The copy is
// built in an owner-only temp directory and linked into place as its last step,
// so it never exists world-readable even for the instant before a chmod; a path
// that already exists is refused, because VACUUM INTO would overwrite it.
func (d *DB) BackupTo(path string) error {
	if err := vacuumInto(d.sqlDB, path); err != nil {
		return fmt.Errorf("db: backup to %s: %w", path, err)
	}
	return nil
}

// vacuumInto copies the whole database to target with the engine's VACUUM INTO.
// The copy lands in an owner-only temp directory beside the target, so a
// failure never leaves a world-readable file, and is hard-linked to target:
// the link fails rather than clobbering a target that appeared meanwhile.
func vacuumInto(pool *sql.DB, target string) error {
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("db: vacuum into %s: file already exists: %w", target, ErrInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("db: vacuum into %s: %w", target, err)
	}

	// Refuse a target the engine's VACUUM INTO literal cannot express before
	// anything is created, so the failure names the destination the caller
	// asked for rather than the private temp copy. The quote refusal is the
	// driver-independent half, checked here in Go so it is the same answer on
	// either engine; the literal's own refusal follows it as defence in depth.
	if err := refuseQuotedTarget(target); err != nil {
		return err
	}
	if _, _, err := vacuumIntoStmt(target); err != nil {
		return err
	}

	dir, err := mkdirVacuumTemp(filepath.Dir(target), ".vacuum-")
	if err != nil {
		return fmt.Errorf("db: vacuum into %s: temp dir: %w", target, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("db: vacuum into %s: chmod temp dir: %w", target, err)
	}

	copyPath := filepath.Join(dir, "copy")
	query, args, err := vacuumIntoStmt(copyPath)
	if err != nil {
		return err
	}
	if _, err := pool.ExecContext(context.Background(), query, args...); err != nil {
		return fmt.Errorf("db: vacuum into %s: %w", target, mapBusy(err))
	}
	if err := chmodPrivate(copyPath); err != nil {
		return fmt.Errorf("db: vacuum into %s: chmod: %w", target, err)
	}
	if err := os.Link(copyPath, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("db: vacuum into %s: file already exists: %w", target, ErrInvalid)
		}
		return fmt.Errorf("db: vacuum into %s: link: %w", target, err)
	}
	return nil
}

// refuseQuotedTarget refuses a target holding a quote character, which the
// copy's literal form cannot express. It is answered here rather than left to
// the engine because only one engine's literal can refuse: Turso's takes the
// target as a literal and rejects a quote, while modernc's takes it as a
// bound parameter and accepts one. Deciding in Go makes the refusal the same
// whichever engine compiled the binary, and it still names the destination the
// caller asked for rather than the private temp copy.
func refuseQuotedTarget(target string) error {
	if strings.Contains(target, "'") {
		return fmt.Errorf("db: vacuum into %s: the path contains a quote: %w", target, ErrInvalid)
	}
	return nil
}

// Vacuum compacts the database in place, outside any transaction, as sqlite
// requires. It refuses when this is not the only direct handle on the file: the
// swap closes and reopens the pool, which would strand every other handle.
func (d *DB) Vacuum() error {
	if err := d.vacuumAllowed(); err != nil {
		return err
	}
	if err := d.walCheckpoint(); err != nil {
		return err
	}

	sibling := d.path + ".vacuum"
	if err := os.Remove(sibling); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("db: vacuum: remove stale %s: %w", sibling, err)
	}
	if err := vacuumInto(d.sqlDB, sibling); err != nil {
		return fmt.Errorf("db: vacuum: %w", err)
	}
	return d.swapInVacuum(sibling)
}

// vacuumAllowed refuses the handle kinds a vacuum cannot swap under.
//
// A sync member is refused here rather than at the checkpoint below it, because
// the swap is the worse of the two: renaming a fresh file over the database
// discards the write-ahead log the sync engine holds a watermark into, and
// leaves the engine addressing a file that is not the one it opened.
func (d *DB) vacuumAllowed() error {
	switch {
	case d.served:
		return fmt.Errorf("db: vacuum: the owner serves this handle: %w", ErrInvalid)
	case d.path == "":
		return fmt.Errorf("db: vacuum: not a direct handle: %w", ErrInvalid)
	case handleCount(d.path) > 1:
		return fmt.Errorf("db: vacuum: %d direct handles are open on %s: %w", handleCount(d.path), d.path, ErrInvalid)
	}
	return d.requireWritableMember("vacuum")
}

// swapInVacuum replaces the database file with the vacuumed sibling and reopens
// the pool. Every failure after the close reopens the original, so the handle
// keeps serving the file it had.
func (d *DB) swapInVacuum(sibling string) error {
	if err := d.sqlDB.Close(); err != nil {
		return fmt.Errorf("db: vacuum: close: %w", err)
	}
	if err := requireDrainedWAL(d.path); err != nil {
		return d.reopenOriginal(err)
	}
	if err := os.Rename(sibling, d.path); err != nil {
		return d.reopenOriginal(fmt.Errorf("db: vacuum: rename %s: %w", sibling, err))
	}
	// The old -shm describes the replaced file and would confuse the reopen.
	_ = os.Remove(d.path + "-shm")

	pool, err := openPool(d.path, d.busy, false)
	if err != nil {
		return d.reopenOriginal(fmt.Errorf("db: vacuum: reopen: %w", err))
	}
	d.sqlDB = pool
	return nil
}

// requireDrainedWAL refuses the swap while the write-ahead log still holds
// pages: renaming the database over the file would drop them.
func requireDrainedWAL(path string) error {
	n, err := walBytes(path)
	if err != nil {
		return fmt.Errorf("db: vacuum: stat -wal: %w", err)
	}
	if n == 0 {
		return nil
	}
	return fmt.Errorf("db: vacuum: the -wal still holds %d bytes: %w", n, ErrInvalid)
}

// walBytes reports how many bytes the write-ahead log beside path still holds.
// An absent log holds none, which is the same answer an empty one gives: the two
// callers differ in what they do about it, not in what they measure.
func walBytes(path string) (int64, error) {
	fi, err := os.Stat(path + "-wal")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return fi.Size(), nil
}

// reopenOriginal restores d's pool after a failed vacuum, joining a reopen
// failure to the cause so neither is lost.
func (d *DB) reopenOriginal(cause error) error {
	pool, err := openPool(d.path, d.busy, false)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("db: vacuum: reopen %s: %w: %w", d.path, ErrOpen, err))
	}
	d.sqlDB = pool
	return cause
}
