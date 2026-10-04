package db

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// seedPageSize is the page size Turso's upload path asserts of the file it
// uploads. Nothing in the tree sets it, so the default is the only value a
// database here ever has; the assertion is what catches a file that came from
// elsewhere, and it is a refusal rather than a rebuild because a page size is
// fixed at creation and no pragma can change one.
const seedPageSize = 4096

// seedJournalMode is the journal mode the upload path asserts.
const seedJournalMode = "wal"

// UploadShape is the shape Turso's upload path asserts of a file before it
// uploads it: a write-ahead log, 4096-byte pages, and a log holding nothing.
// WALBytes is what the log still holds, so the report can name the number rather
// than only that the check failed.
type UploadShape struct {
	JournalMode string `json:"journal_mode"`
	PageSize    int64  `json:"page_size"`
	WALBytes    int64  `json:"wal_bytes"`
}

// Satisfied reports whether all three assertions hold.
func (s UploadShape) Satisfied() bool { return s.Failing() == "" }

// Failing names the first assertion that does not hold, or the empty string
// when all three do. The order is the order the upload path makes them in.
func (s UploadShape) Failing() string {
	switch {
	case s.JournalMode != seedJournalMode:
		return fmt.Sprintf("journal_mode is %q, not %s", s.JournalMode, seedJournalMode)
	case s.PageSize != seedPageSize:
		return fmt.Sprintf("page_size is %d, not %d", s.PageSize, seedPageSize)
	case s.WALBytes != 0:
		return fmt.Sprintf("the write-ahead log still holds %d bytes", s.WALBytes)
	}
	return ""
}

// UploadShape reads the three assertions off this handle. A handle with no file
// behind it cannot report a log, so it reports one that holds nothing rather
// than a shape it did not measure.
func (d *DB) UploadShape() (UploadShape, error) {
	var shape UploadShape
	if err := d.sqlDB.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&shape.JournalMode); err != nil {
		return UploadShape{}, fmt.Errorf("db: upload shape: journal_mode: %w", mapBusy(err))
	}
	if err := d.sqlDB.QueryRowContext(context.Background(), `PRAGMA page_size`).Scan(&shape.PageSize); err != nil {
		return UploadShape{}, fmt.Errorf("db: upload shape: page_size: %w", mapBusy(err))
	}
	if d.path == "" {
		return shape, nil
	}
	bytes, err := walBytes(d.path)
	if err != nil {
		return UploadShape{}, fmt.Errorf("db: upload shape: %w", err)
	}
	shape.WALBytes = bytes
	return shape, nil
}

// SeedCopy writes the copy of this database that the upload path takes, and
// refuses unless the copy carries the shape that path asserts: a write-ahead
// log, 4096-byte pages, and a log holding nothing.
//
// It needs the file, so it refuses a dialled handle rather than measuring a
// shape it cannot see. It never converts anything: a database enabling late must
// cost one upload, not a second conversion of the history it already converted.
// The two things it can bring about it brings about before it measures -- the
// mode is set with the pragma every pooled connection already runs, and the log
// is drained by the truncate checkpoint that makes the copy compact -- and the
// assertion has the last word on both. A page size no pragma can change is
// refused, not worked around.
//
// The copy lands through the same owner-only temp directory and hard link a
// backup uses, so it is never world-readable even for the instant before its
// chmod, and a path that already exists is refused rather than overwritten: a
// second seed over the first is exactly the rework this path exists to avoid.
// The quote-in-path refusal comes from the same literal the copy is written
// with, before anything is created.
func (d *DB) SeedCopy(path string) error {
	if d.path == "" {
		return fmt.Errorf("db: seed copy: %s: not a direct handle: %w", path, ErrInvalid)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("db: seed copy: %s already exists, so the seed would be written twice: %w", path, ErrInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("db: seed copy: %s: %w", path, err)
	}

	if err := d.prepareUploadShape(); err != nil {
		return err
	}
	shape, err := d.UploadShape()
	if err != nil {
		return err
	}
	if failing := shape.Failing(); failing != "" {
		return fmt.Errorf("db: seed copy: %s cannot reach the upload shape: %s: %w", d.path, failing, ErrInvalid)
	}
	if err := vacuumInto(d.sqlDB, path); err != nil {
		return fmt.Errorf("db: seed copy: %w", err)
	}
	return assertSeedCopy(path)
}

// prepareUploadShape brings about the two assertions it can: the journal mode
// the pool's own pragma sets, and a drained log. It reports what it did rather
// than measuring, so the caller's assertion is the only thing that decides.
func (d *DB) prepareUploadShape() error {
	if _, err := d.sqlDB.ExecContext(context.Background(), `PRAGMA journal_mode = `+seedJournalMode); err != nil {
		return fmt.Errorf("db: seed copy: set the journal mode: %w", mapBusy(err))
	}
	if err := d.walCheckpoint(); err != nil {
		return err
	}
	return nil
}

// assertSeedCopy checks the copy that was written: it exists, is not empty, and
// left no log of its own beside it. The upload path asserts the same three
// things and would refuse the copy there, where nothing local points at what
// went wrong.
func assertSeedCopy(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("db: seed copy: stat %s: %w", path, err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("db: seed copy: %s is empty: %w", path, ErrInvalid)
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		return fmt.Errorf("db: seed copy: %s left a -wal beside it: %w", path, ErrInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("db: seed copy: stat %s-wal: %w", path, err)
	}
	return nil
}
