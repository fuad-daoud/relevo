package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	// This package is the only place that knows it is sqlite today and Turso
	// tomorrow, so its driver is imported here and nowhere else.
	"modernc.org/sqlite"
)

// rfc3339Milli is the text encoding every timestamp uses in the db.
const rfc3339Milli = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(rfc3339Milli) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(rfc3339Milli, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

const sqliteBusy = 5

// busyTimeoutMS is the busy_timeout pragma every connection opens with, in
// milliseconds; a var so a test can shrink it.
var busyTimeoutMS = 5000

// beginRetryFor bounds how long Tx keeps retrying a busy BEGIN IMMEDIATE.
var beginRetryFor = 30 * time.Second

// DB is a connection to relevo's sqlite database; construct one with Open.
type DB struct {
	sqlDB *sql.DB
	// newer is true when the schema is newer than this binary's migrations.
	newer bool
	have  int // schema version on disk
	know  int // highest migration this binary embeds

	// beginRetry bounds how long Tx retries a busy BEGIN IMMEDIATE; zero, as
	// on a read-only handle, means beginRetryFor.
	beginRetry time.Duration

	// origin is the installation id this handle scopes its records to. Empty
	// means the installation file's id is unknown -- tests, and the peek
	// runtime -- and every scoped query then matches only rows with an empty
	// origin, exactly as before the column existed.
	origin string
}

// Options tunes OpenWith. A negative value is treated as 0, which selects the
// package default; an empty Options opens exactly as Open does.
type Options struct {
	// BusyTimeout is the per-connection busy_timeout; 0 selects busyTimeoutMS.
	BusyTimeout time.Duration
	// BeginRetry bounds how long Tx retries a busy BEGIN IMMEDIATE; 0 selects
	// beginRetryFor.
	BeginRetry time.Duration
	// Origin is the installation id stamped on every row this handle writes
	// and scoped to on every scoped read. Empty leaves the handle unscoped.
	Origin string
}

// journalSizeLimit caps the -wal file after a checkpoint resets it, in bytes;
// without it sqlite keeps a write burst's high-water size for the process's life.
const journalSizeLimit = 64 << 20

// Open opens (creating if needed) the sqlite database at path, applying
// pending migrations. A database whose schema is newer than this binary's is
// left untouched, so an older relevo cannot downgrade it.
func Open(path string) (*DB, error) {
	return OpenWith(path, Options{})
}

// OpenWith opens path like Open, with the busy waits tunable so a caller that
// must fail fast does not wait out the defaults.
func OpenWith(path string, o Options) (*DB, error) {
	return open(path, o)
}

func open(path string, o Options) (_ *DB, err error) {
	seedFromTemplate(path)
	busy := busyTimeoutMS
	if o.BusyTimeout > 0 {
		busy = int(o.BusyTimeout.Milliseconds())
	}
	retry := beginRetryFor
	if o.BeginRetry > 0 {
		retry = o.BeginRetry
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=journal_size_limit(%d)", path, busy, journalSizeLimit)

	// Create the file ourselves first, so it -- and the -wal and -shm siblings
	// sqlite derives from its mode -- is owner-only from the instant it exists,
	// not only after the chmods below.
	if err = ensurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("db: open %s: create: %w: %w", path, ErrOpen, err)
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w: %w", path, ErrOpen, err)
	}
	// Secrets live in this file, so a failure to make it and its WAL siblings
	// owner-only fails the open: a world-readable secrets store is not a
	// warning.
	defer func() {
		if err != nil {
			if cerr := sqlDB.Close(); cerr != nil {
				err = fmt.Errorf("%w, and close failed: %w", err, cerr)
			}
		}
	}()

	if err = ping(sqlDB); err != nil {
		return nil, fmt.Errorf("db: open %s: ping: %w: %w", path, ErrOpen, err)
	}
	if err = chmodPrivate(path); err != nil {
		return nil, fmt.Errorf("db: open %s: chmod: %w: %w", path, ErrOpen, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = chmodIfExists(path + suffix); err != nil {
			return nil, fmt.Errorf("db: open %s: chmod %s: %w: %w", path, suffix, ErrOpen, err)
		}
	}

	have, err := maxVersion(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: version: %w: %w", path, ErrOpen, err)
	}
	know, err := maxEmbedded(migrationFiles)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: migrations: %w: %w", path, ErrOpen, err)
	}
	if have > know {
		return &DB{sqlDB: sqlDB, beginRetry: retry, newer: true, have: have, know: know, origin: o.Origin}, nil
	}
	// A current schema needs no write: BEGIN IMMEDIATE here failed under load.
	if have == know {
		return &DB{sqlDB: sqlDB, beginRetry: retry, have: have, know: know, origin: o.Origin}, nil
	}

	if err = applyMigrations(sqlDB, migrationFiles); err != nil {
		return nil, fmt.Errorf("db: open %s: migrate: %w: %w", path, ErrOpen, err)
	}

	have, err = maxVersion(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: version: %w: %w", path, ErrOpen, err)
	}

	return &DB{sqlDB: sqlDB, beginRetry: retry, have: have, know: know, origin: o.Origin}, nil
}

// Newer reports whether the database's schema is newer than this relevo's.
func (d *DB) Newer() bool { return d.newer }

func (d *DB) SchemaVersions() (have, know int) { return d.have, d.know }

// CheckMigrate refuses a database whose schema is newer than this relevo, so
// `relevo db migrate` does not touch it.
func (d *DB) CheckMigrate() error {
	if !d.newer {
		return nil
	}
	return fmt.Errorf("schema version %d is newer than this relevo (knows %d): upgrade relevo: %w", d.have, d.know, ErrNewerSchema)
}

// ping establishes the first connection, retrying while sqlite reports the
// database busy. Two processes opening a fresh database both try to switch it
// to WAL in the DSN, and sqlite does not invoke the busy handler for a
// journal_mode change, so the loser gets SQLITE_BUSY and must retry.
func ping(sqlDB *sql.DB) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		err = sqlDB.Ping()
		if err == nil || !errors.Is(mapBusy(err), ErrBusy) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func (d *DB) Close() error {
	if err := d.sqlDB.Close(); err != nil {
		return fmt.Errorf("db: close: %w", err)
	}
	return nil
}

// BackupTo copies the whole database to path with VACUUM INTO. The target is
// pre-created owner-only, so the copy never exists world-readable even for the
// instant before the chmod; a path that already exists is refused, because
// VACUUM INTO would overwrite it.
func (d *DB) BackupTo(path string) error {
	if err := createFile(path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("db: backup to %s: file already exists: %w", path, ErrInvalid)
		}
		return fmt.Errorf("db: backup to %s: %w", path, mapBusy(err))
	}

	if _, err := d.sqlDB.ExecContext(context.Background(), `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("db: backup to %s: %w", path, mapBusy(err))
	}
	if err := chmodPrivate(path); err != nil {
		return fmt.Errorf("db: backup to %s: chmod: %w", path, err)
	}
	return nil
}

// Vacuum compacts the database in place. It runs outside any transaction, as
// sqlite requires.
func (d *DB) Vacuum() error {
	if _, err := d.sqlDB.ExecContext(context.Background(), `VACUUM`); err != nil {
		return fmt.Errorf("db: vacuum: %w", mapBusy(err))
	}
	return nil
}

func (d *DB) Version() (int, error) {
	var v sql.Null[int64]
	if err := d.sqlDB.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("db: version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.V), nil
}

// Tx is a locked view of the database inside one BEGIN IMMEDIATE transaction;
// the *DB forms wrap one Tx each.
type Tx struct {
	conn *sql.Conn
	ctx  context.Context
	// origin is the opening DB's installation id; a Tx always scopes and
	// stamps like the handle it came from.
	origin string
}

// Tx runs fn inside one BEGIN IMMEDIATE transaction: commit on a nil return,
// rollback otherwise. A busy BEGIN IMMEDIATE is retried until the DB's retry
// window has elapsed since the first attempt; COMMIT is not retried. A
// database whose schema is newer than this binary is never written.
func (d *DB) Tx(fn func(*Tx) error) error {
	if d.newer {
		return fmt.Errorf("db: tx: schema version %d is newer than this relevo (knows %d); refusing to write: %w", d.have, d.know, ErrNewerSchema)
	}
	return d.tx(context.Background(), fn)
}

func (d *DB) tx(ctx context.Context, fn func(*Tx) error) (err error) {
	conn, err := d.sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("db: tx: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("db: tx close: %w", cerr)
		}
	}()

	// BEGIN IMMEDIATE takes the write lock at once, so a competing writer
	// fails busy instead of blocking. fn never runs until BEGIN succeeds, so it
	// runs at most once.
	retryFor := d.beginRetry
	if retryFor <= 0 {
		retryFor = beginRetryFor
	}
	start := time.Now()
	for {
		_, beginErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if beginErr == nil {
			break
		}
		mapped := mapBusy(beginErr)
		if !errors.Is(mapped, ErrBusy) || time.Since(start) >= retryFor {
			return fmt.Errorf("db: tx begin: %w", mapped)
		}
		time.Sleep(time.Duration(25+rand.Intn(76)) * time.Millisecond)
	}

	if txErr := fn(&Tx{conn: conn, ctx: ctx, origin: d.origin}); txErr != nil {
		if _, rerr := conn.ExecContext(ctx, "ROLLBACK"); rerr != nil {
			return fmt.Errorf("db: tx: rollback failed: %w", errors.Join(txErr, rerr))
		}
		return txErr
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("db: tx commit: %w", mapBusy(err))
	}
	return nil
}

// mapBusy turns a driver's SQLITE_BUSY into ErrBusy.
func mapBusy(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteBusy {
		return ErrBusy
	}
	if strings.Contains(err.Error(), "database is locked") {
		return ErrBusy
	}
	return err
}

// createFileMode is the mode a fresh database or backup target is created
// with: owner-only, because the database holds secrets. Creating the file first
// also gives sqlite a mode to derive its -wal and -shm siblings from.
const createFileMode = 0o600

// createFile creates path owner-only before sqlite sees it. O_EXCL makes a file
// that appeared meanwhile -- a concurrent first open, or an existing backup
// target -- an os.ErrExist rather than a clobber; the caller decides what that
// means. It is a var so a test can record the mode the file is created with,
// before the post-hoc chmod can mask it.
var createFile = func(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, createFileMode)
	if err != nil {
		return err
	}
	return f.Close()
}

// ensurePrivateFile creates path owner-only when it is absent. A concurrent
// first open that wins the race is not an error: the other process's file is
// the database. Any other error is returned.
func ensurePrivateFile(path string) error {
	switch _, err := os.Stat(path); {
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := createFile(path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// chmodPrivate makes path owner-only; the database holds secrets.
func chmodPrivate(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return nil
}

// chmodIfExists makes path owner-only when it exists; sqlite creates the -wal
// and -shm siblings lazily.
func chmodIfExists(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func (t *Tx) exec(query string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(t.ctx, query, args...)
}

func (t *Tx) queryRow(query string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(t.ctx, query, args...)
}

// queryer is the slice of *sql.DB and *sql.Conn that readers need, so one
// query function backs both *DB and *Tx reads.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
