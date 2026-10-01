//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
	convertMarker     = "marker"
	convertClosed     = "closed"
	convertSidecars   = "sidecars"
)

// relevoApplicationID is the marker a converted file carries: the SQLite
// header's application_id field. It lives in the file, so it survives VACUUM
// INTO and a plain copy, and a raw read of the header needs no engine to open
// the database. SQLite never sets the field on its own and nothing else in
// relevo uses it, so the value cannot mean anything but "already converted".
// 0x52454C56 spells "RELV".
const relevoApplicationID = 0x52454C56

// sqliteMagic is the 16 bytes every SQLite database starts with, and
// applicationIDOffset is where the header's application_id field sits. A file
// without the magic has no schema yet: it is a fresh database, not a legacy one.
const (
	sqliteMagic         = "SQLite format 3\x00"
	sqliteHeaderLen     = 100
	applicationIDOffset = 68
)

// convertLegacy turns a database an earlier, modernc build of relevo wrote into
// one Turso can open. It runs once and is safe if the process is killed at any
// step: every step is idempotent, and each leaves a consistent file on disk.
//
// The trigger is the marker the conversion leaves in the file, plus the -shm
// sidecar. A file without the marker was written by a pre-Turso build and must
// be converted -- including one a pre-Turso build closed cleanly, which deletes
// its -shm and would otherwise be skipped. A -shm beside the file means a SQLite
// process left it, such as an unclean stop after a downgrade, and the marker is
// then not trusted. A file with no SQLite header at all is not a legacy database
// -- it is one the Turso build is creating -- so it is left for the engine to
// mark at creation, and never converted.
//
// The steps:
//
//   - back the file up to path+".pre-turso" through VACUUM INTO, unless a
//     backup already exists (a killed earlier attempt leaves one to reuse);
//   - open under modernc and drain the -wal with PRAGMA wal_checkpoint(TRUNCATE),
//     requiring the checkpoint not to be busy;
//   - repair every invalid UTF-8 TEXT value, which only modernc can read;
//   - set the marker, so a later open treats the file as converted;
//   - close, so SQLite checkpoints and cleans its sidecars;
//   - refuse if the -wal still holds frames, and remove a leftover -shm.
func convertLegacy(path string) error {
	shm, err := pathExists(path + "-shm")
	if err != nil {
		return fmt.Errorf("db: convert %s: stat -shm: %w", path, err)
	}
	marked, fresh, err := fileMarker(path)
	if err != nil {
		return fmt.Errorf("db: convert %s: read the header: %w", path, err)
	}
	if fresh || (!shm && marked) {
		return nil
	}

	pool, err := openConvertPool(path)
	if err != nil {
		return fmt.Errorf("db: convert %s: open modernc: %w: %w", path, ErrOpen, err)
	}
	if !shm {
		// The header holds the marker only once SQLite has checkpointed it. A
		// write that died before its checkpoint leaves the marker in the -wal,
		// where the raw read cannot see it and only the engine can, so the
		// marker is confirmed through the engine before the file is rewritten.
		converted, err := engineMarked(pool)
		if err != nil {
			_ = pool.Close()
			return fmt.Errorf("db: convert %s: read application_id: %w", path, err)
		}
		if converted {
			return pool.Close()
		}
	}
	return runConversion(path, pool)
}

// runConversion performs the conversion's steps in order on a pool opened under
// modernc. A hook abort is a kill: it returns the hook's error and leaves the
// pool open, so SQLite's sidecars survive on disk exactly as a kill would leave
// them.
func runConversion(path string, pool *sql.DB) error {
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
	if err := stepHook(convertRepair); err != nil {
		_ = conn.Close()
		return err
	}
	// The marker goes in after the repair, never before: a kill between the two
	// leaves the file unmarked, so the next open converts it again instead of
	// trusting a half-done conversion.
	if err := markConverted(ctx, conn); err != nil {
		_ = conn.Close()
		return fail(fmt.Errorf("db: convert %s: %w", path, err))
	}
	if err := stepHook(convertMarker); err != nil {
		_ = conn.Close()
		return err
	}

	if err := conn.Close(); err != nil {
		return fail(fmt.Errorf("db: convert %s: close conn: %w", path, err))
	}
	if err := pool.Close(); err != nil {
		return fmt.Errorf("db: convert %s: close: %w", path, err)
	}
	if err := stepHook(convertClosed); err != nil {
		return err
	}
	return finishSidecars(path)
}

// finishSidecars refuses a -wal that still holds frames -- opening under Turso
// would lose them -- and removes the -shm SQLite left behind, which is the last
// trace of the old mode and would make the next open convert again.
func finishSidecars(path string) error {
	if size, err := walSize(path); err != nil {
		return fmt.Errorf("db: convert %s: stat -wal: %w", path, err)
	} else if size > 0 {
		return fmt.Errorf("db: convert %s: the -wal still holds %d bytes: %w", path, size, ErrOpen)
	}
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

// fileMarker reads the marker straight out of the file header. It reports
// converted for a file carrying the marker, and fresh for a file that is not a
// SQLite database yet -- absent, empty or truncated -- which is the state a new
// database is in before the Turso build creates it.
func fileMarker(path string) (converted, fresh bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, true, nil
		}
		return false, false, err
	}
	defer func() { _ = f.Close() }()

	header := make([]byte, sqliteHeaderLen)
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, false, err
	}
	if n < sqliteHeaderLen || string(header[:len(sqliteMagic)]) != sqliteMagic {
		return false, true, nil
	}
	id := int64(binary.BigEndian.Uint32(header[applicationIDOffset : applicationIDOffset+4]))
	return id == relevoApplicationID, false, nil
}

// markConverted writes the marker through the conversion's connection and
// drains the -wal again, so the marker is in the main file rather than only in a
// log another handle can keep alive: a kill right after this leaves a file the
// header alone already reads as converted, and the sidecar check below still
// sees a drained -wal.
func markConverted(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, markerStatement()); err != nil {
		return fmt.Errorf("set application_id: %w", err)
	}
	busy, err := checkpointTruncate(ctx, conn)
	if err != nil {
		return fmt.Errorf("checkpoint the marker: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("the -wal is held by another connection: %w", ErrOpen)
	}
	return nil
}

// markerStatement is the statement that writes the marker; the value is a
// compiled-in constant, so no statement text can reach the pragma.
func markerStatement() string {
	return fmt.Sprintf("PRAGMA application_id = %d", relevoApplicationID)
}

// engineMarked reports whether the open pool sees the marker, which covers one
// a write that never checkpointed left in the -wal.
func engineMarked(pool *sql.DB) (bool, error) {
	ctx := context.Background()
	conn, err := pool.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	var id int64
	if err := conn.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return false, err
	}
	return id == relevoApplicationID, nil
}

// markFreshDatabase records the marker on a database the engine is creating, so
// a file this build writes is never taken for a legacy one. The marker is
// checkpointed into the main file straight away: left in the -wal, the next open
// of the same file in this process would read the header and not find it. It
// runs under the live engine, so only Turso's own write path is involved.
func markFreshDatabase(pool *sql.DB) error {
	ctx := context.Background()
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, markerStatement()); err != nil {
		return fmt.Errorf("set application_id: %w", err)
	}
	// A checkpoint another pooled connection keeps from running is not an error
	// here: the marker is idempotent, and a database still without the header is
	// marked again by the next open that reads it as fresh.
	if _, err := checkpointTruncate(ctx, conn); err != nil {
		return fmt.Errorf("checkpoint the marker: %w", err)
	}
	return nil
}

// requireConverted refuses a file whose header lacks the conversion marker. A
// read-only open must not convert -- that is a write -- so it refuses and leaves
// the file for a writable open, the daemon's, to convert and mark.
func requireConverted(path string) error {
	converted, _, err := fileMarker(path)
	if err != nil {
		return fmt.Errorf("db: open readonly %s: read the header: %w", path, err)
	}
	if !converted {
		return fmt.Errorf("db: open readonly %s: %w", path, ErrNotConverted)
	}
	return nil
}

// pathExists reports whether path names an existing file.
func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, err
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
// flag: zero when the whole -wal was checkpointed. The columns are read as
// nullable because the engines report NULL rather than a number when the
// checkpoint cannot run at all; that is not a drained -wal, so it is reported as
// busy and a caller that requires one refuses.
func checkpointTruncate(ctx context.Context, conn *sql.Conn) (int, error) {
	var busy, log, checkpointed sql.NullInt64
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return 0, err
	}
	if !busy.Valid {
		return 1, nil
	}
	return int(busy.Int64), nil
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
