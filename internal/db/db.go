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

	"github.com/fuad-daoud/relevo/internal/db/wire"
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

	// route is how this handle reaches the database: "file" for a direct open,
	// "owner <sock>" for a dial. It is what `relevo doctor` reports and what a
	// test asserts about the switch.
	route string

	// path is the file a direct handle opened; empty on a dialled handle. It is
	// what Vacuum reopens and what the per-path handle count keys on.
	path string
	// busy is the busy_timeout the pool opens connections with, kept so a
	// reopen after a vacuum uses the same value.
	busy time.Duration
	// served is true while an owner serves this handle: a vacuum must not close
	// the pool out from under its clients.
	served bool
	// readOnly is true on a handle that must not write. A read-only open sets
	// it, and Close then skips the checkpoint that would otherwise write
	// through a verb advertised as read-only.
	readOnly bool
	// onClosed, when set, runs after this handle's pool is closed. The test
	// owner hop uses it to learn that a dialled client handle is gone.
	onClosed func()

	// local is the machine-local file beside this handle, opened by OpenSplit
	// and nil on every other handle. The two run the same migrations, so it is
	// the destination for the rows that never leave this machine.
	local *DB
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
	// AdHoc marks every connection this handle's pool opens as an ad-hoc read:
	// the owner may refuse such a request while it reaps an abandoned
	// statement. A handle opened without it is never refused.
	AdHoc bool
}

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

// OpenRaw opens path with the selected engine's pool and its per-connection
// settings, without migrating the file, without an owner hop, and without
// joining the per-path handle count. It is the seam tests and tooling use to
// drive the driver directly.
func OpenRaw(path string) (*sql.DB, error) {
	return openPool(path, time.Duration(busyTimeoutMS)*time.Millisecond, false)
}

// open routes through the test-only owner hop when one is installed, and opens
// the file directly otherwise.
func open(path string, o Options) (*DB, error) {
	if hop := ownerHop; hop != nil {
		sock, err := hop(path, o, func() (*DB, error) { return openDirect(path, o) })
		if err != nil {
			return nil, err
		}
		d, err := dial(sock, o, false)
		if err != nil {
			return nil, err
		}
		if onClosed := ownerHopClosed; onClosed != nil {
			d.onClosed = func() { onClosed(sock) }
		}
		return d, nil
	}
	return openDirect(path, o)
}

func openDirect(path string, o Options) (_ *DB, err error) {
	seedFromTemplate(path)
	busy := time.Duration(busyTimeoutMS) * time.Millisecond
	if o.BusyTimeout > 0 {
		busy = o.BusyTimeout
	}
	retry := beginRetryFor
	if o.BeginRetry > 0 {
		retry = o.BeginRetry
	}

	// Create the file ourselves first, so it -- and the -wal and -shm siblings
	// the engine derives from its mode -- is owner-only from the instant it
	// exists, not only after the chmods below.
	if err = ensurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("db: open %s: create: %w: %w", path, ErrOpen, err)
	}

	// The relevo open lock: one process opens the path at a time, and every
	// handle in this process on this path shares the one lock. It is taken
	// before the one-time conversion, which reads and rewrites the file.
	first, err := acquireHandle(path, true)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			releaseHandle(path)
		}
	}()
	// This handle's engine open is the only one allowed to create a fresh
	// file's first page; a later handle waits for it to finish before it opens a
	// pool of its own, so two pools never write the new file at once. The signal
	// is deferred before the release above runs, so a waiting opener wakes even
	// when this open fails.
	if first {
		defer signalCreated(path)
	} else {
		awaitCreated(path)
	}

	// A file an earlier, modernc build wrote is converted once here, under
	// modernc, before the engine opens it; a file the engine already wrote is
	// left alone.
	if err = convertLegacy(path); err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}

	sqlDB, err := openPool(path, busy, false)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w: %w", path, engineSentinel(err), err)
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
		return nil, fmt.Errorf("db: open %s: ping: %w: %w", path, engineSentinel(err), err)
	}
	if err = chmodPrivate(path); err != nil {
		return nil, fmt.Errorf("db: open %s: chmod: %w: %w", path, ErrOpen, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = chmodIfExists(path + suffix); err != nil {
			return nil, fmt.Errorf("db: open %s: chmod %s: %w: %w", path, suffix, ErrOpen, err)
		}
	}

	return finishDirectOpen(sqlDB, path, o, busy, retry)
}

// finishDirectOpen settles the schema on a pool that is already open: a file
// whose schema is newer than this binary is left untouched, a current file needs
// no write at all, and an older one is migrated.
func finishDirectOpen(sqlDB *sql.DB, path string, o Options, busy, retry time.Duration) (*DB, error) {
	have, err := maxVersion(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: version: %w: %w", path, ErrOpen, err)
	}
	know, err := maxEmbedded(migrationFiles)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: migrations: %w: %w", path, ErrOpen, err)
	}
	handle := func(have int, newer bool) *DB {
		return &DB{sqlDB: sqlDB, beginRetry: retry, newer: newer, have: have, know: know, origin: o.Origin, route: "file", path: path, busy: busy}
	}
	if have > know {
		return handle(have, true), nil
	}
	// A current schema needs no write: BEGIN IMMEDIATE here failed under load.
	if have == know {
		return handle(have, false), nil
	}

	if err := applyMigrations(sqlDB, migrationFiles); err != nil {
		return nil, fmt.Errorf("db: open %s: migrate: %w: %w", path, ErrOpen, err)
	}
	have, err = maxVersion(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: version: %w: %w", path, ErrOpen, err)
	}
	return handle(have, false), nil
}

// Newer reports whether the database's schema is newer than this relevo's.
func (d *DB) Newer() bool { return d.newer }

// Route names how this handle reaches the database: "file" for a direct open,
// "owner <sock>" for a dial.
func (d *DB) Route() string { return d.route }

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

// Close checkpoints a writable direct handle, closes its pool, releases the
// path's handle and flock, then notifies onClosed. The path is cleared first, so
// a second Close is a no-op, and the handle is released on the way out even when
// the pool close errors: leaving the flock held after a failed close would wedge
// every later opener.
func (d *DB) Close() error {
	if local := d.local; local != nil {
		d.local = nil
		if lerr := local.Close(); lerr != nil {
			return fmt.Errorf("db: close local file: %w", lerr)
		}
	}
	path := d.path
	d.path = ""
	if path != "" && !d.readOnly {
		// Turso keeps the write-ahead log across a close, so checkpoint it
		// here: callers and the template seeder copy the main file alone, and
		// a non-empty -wal would leave that copy without its schema. A
		// read-only handle must not write, so it never checkpoints.
		_ = d.walCheckpoint()
	}
	closeErr := d.sqlDB.Close()
	if path != "" {
		// The path and its flock are released only after the pool is closed,
		// so a concurrent opener cannot win the flock while this handle's own
		// connections still hold the engine's file lock.
		releaseHandle(path)
	}
	d.runOnClosed()
	if closeErr != nil {
		return fmt.Errorf("db: close: %w", closeErr)
	}
	return nil
}

// runOnClosed notifies the close observer installed on a dialled handle, once.
func (d *DB) runOnClosed() {
	if d.onClosed == nil {
		return
	}
	onClosed := d.onClosed
	d.onClosed = nil
	onClosed()
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
	// have is the schema version the opening handle saw, so a transaction
	// writes only the columns a database at that version carries.
	have int
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
		// The owner refused the BEGIN before executing it, so a fresh
		// connection retried inside the same window cannot apply it twice.
		if restartingRefusal(beginErr) && time.Since(start) < retryFor {
			_ = conn.Close()
			conn, err = d.sqlDB.Conn(ctx)
			if err != nil {
				return fmt.Errorf("db: tx: %w", err)
			}
			continue
		}
		mapped := mapBusy(beginErr)
		if !errors.Is(mapped, ErrBusy) || time.Since(start) >= retryFor {
			return fmt.Errorf("db: tx begin: %w", mapped)
		}
		time.Sleep(time.Duration(25+rand.Intn(76)) * time.Millisecond)
	}

	if txErr := fn(&Tx{conn: conn, ctx: ctx, origin: d.origin, have: d.have}); txErr != nil {
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

// restartingRefusal reports whether an error is the owner's refusal to run a
// request because it is restarting. The owner guarantees such a request never
// ran, so the caller may retry it on a fresh connection.
func restartingRefusal(err error) bool {
	var ref *wire.Refusal
	return errors.As(err, &ref) && ref.Code == wire.RefuseRestarting
}

// errCode returns the sqlite result code an error carries: the wire's rebuilt
// code on a dialled handle, and the engine's own mapping on a direct one, where
// a Turso error wraps a sentinel instead of exposing a Code method.
func errCode(err error) (code, ext int, ok bool) {
	if code, ok := wire.CodeOf(err); ok {
		return code, wire.ExtendedCodeOf(err), true
	}
	return engineCode(err)
}

// engineSentinel picks the sentinel an engine error from an open carries: a
// lock another process holds is ErrLocked, so a caller can fall back to the
// owner, and everything else is ErrOpen.
func engineSentinel(err error) error {
	if engineLocked(err) {
		return ErrLocked
	}
	return ErrOpen
}

// mapBusy turns a driver's SQLITE_BUSY into ErrBusy. It matches any error
// carrying the code, so a value rebuilt on the client from the wire maps the
// same way the driver's own error does.
func mapBusy(err error) error {
	if err == nil {
		return nil
	}
	if code, _, ok := errCode(err); ok && code == sqliteBusy {
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
