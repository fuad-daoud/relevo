//go:build !modernc

package db

// The dedicated capture connection: one connection held for the change-data-
// capture pragma, on a file the sync engine has joined to a remote.
//
// The pragma is a write, and a write queues for the database's single write
// slot, so putting it on a path every connection takes turns connection setup
// into a write transaction competing with whatever the daemon is already
// writing. Under a contended slot every read then waits out the whole busy
// timeout and fails busy, which is what took a lived-in machine down. So the
// pragma is asked exactly once, on one connection opened for the purpose, and
// never on the per-connection path: openPragmas and pragmaConnector carry no
// trace of it, and a test guards that.
//
// One connection is enough because the driver captures per connection: a write
// made here is captured, and a write made on any other connection over the same
// file is not. So the backfill pass, and anything else that has to land rows in
// the change set, runs through this connection rather than through the pool.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
)

// captureChangesPragma installs a connection's capture state and fills
// turso_cdc from that connection's writes. It creates the capture tables when
// they are absent, which is why a file that is not a member never gets it: the
// tables it creates are the sync driver's marker tables, so asking a bare file
// for it would make this package's own database look joined to a remote.
const captureChangesPragma = "PRAGMA capture_data_changes_conn('full,turso_cdc')"

// captureConn is one held connection carrying the capture pragma. The pragma is
// asked once, when the connection is opened, and every write made over it
// afterwards is recorded in the change set.
type captureConn struct {
	conn *sql.Conn
}

// stepError is what one bounded step of the capture surface reports when it does
// not finish. The step's own name is in the message, so a reader is told whether
// the pool, the pragma or the recorded batch ran out of time rather than being
// left to guess from a path.
//
// A step that gave up on the write slot, or on its own deadline, carries
// ErrContended: what holds it is a transaction that ends, so the failure is
// retryable and must not reach a reader as a defect.
func stepError(what, step string, err error) error {
	mapped := mapBusy(err)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(mapped, ErrBusy) {
		return fmt.Errorf("db: %s: %s: %w: %w", what, step, ErrContended, mapped)
	}
	return fmt.Errorf("db: %s: %s: %w", what, step, mapped)
}

// capturePools holds the one-connection pools capture connections are drawn
// from, one per member path. A pool of its own rather than the handle's is what
// keeps a capturing connection out of the pool a query borrows from.
var capturePools = struct {
	sync.Mutex
	byPath map[string]*sql.DB
}{byPath: map[string]*sql.DB{}}

// openCaptureConnection opens one connection over path with the capture pragma
// installed, and returns it held.
//
// The pool it comes from is bounded to a single connection, so a write slot
// that is not free costs one bounded wait rather than a pile of blocked
// connects. The open is bounded by busy for the same reason: this is a write,
// and an unbounded wait for a contended slot is the failure this connection
// exists to make impossible.
func openCaptureConnection(ctx context.Context, path string, busy int64) (*captureConn, error) {
	pool, err := capturePoolFor(path, busy)
	if err != nil {
		return nil, fmt.Errorf("db: %s: the capture pool: %w", path, mapBusy(err))
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, stepError(path, "the capture connection", err)
	}
	if _, err := conn.ExecContext(ctx, captureChangesPragma); err != nil {
		_ = conn.Close()
		return nil, stepError(path, "the capture pragma", err)
	}
	return &captureConn{conn: conn}, nil
}

// captureOf is the member path's held capture connection, opened once and shared
// by every writer over the file.
//
// One connection rather than one per writer because the pragma is a write and the
// write slot is single: a pool of capture connections would be a pool of
// connections each queuing for the same slot, which is the pile-up the single
// connection exists to prevent. Writes are already serialised -- the store holds
// an exclusive lock across every load-modify-save, and BEGIN IMMEDIATE refuses a
// second writer -- so one connection costs nothing that the write slot was not
// already costing.
var captureHeld = struct {
	sync.Mutex
	byPath map[string]*heldCapture
}{byPath: map[string]*heldCapture{}}

// heldCapture is one path's capture connection and the gate that serialises
// everything written over it.
//
// The gate is a one-token channel rather than a mutex because every caller waits
// on it under a deadline: a mutex would hold a caller past any bound it set, and
// the one caller that cannot be allowed to wait without end is a daemon write
// that the backfill's own batch is holding the connection for.
type heldCapture struct {
	gate chan struct{}
	conn *captureConn
	// borrows counts the open captures over this connection. pinned says a db
	// handle writes through it, so the connection outlives the borrows: a
	// borrow alone releases what it opened when the last one lets go.
	borrows int
	pinned  bool
}

func newHeldCapture(conn *captureConn) *heldCapture {
	h := &heldCapture{conn: conn, gate: make(chan struct{}, 1)}
	h.gate <- struct{}{}
	return h
}

// acquire takes the connection for one writer, and the func that hands it back.
// A caller that gave up waiting is told so rather than left holding a token.
func (h *heldCapture) acquire(ctx context.Context) (*sql.Conn, func(), error) {
	select {
	case <-h.gate:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	conn := h.conn.Conn()
	if conn == nil {
		h.gate <- struct{}{}
		return nil, nil, fmt.Errorf("the capture connection is closed: %w", ErrOpen)
	}
	return conn, func() { h.gate <- struct{}{} }, nil
}

// captureConnFor is the held capture connection over a member path, opening it if
// this is the first writer. It is a no-op for a file that is not a member: a
// non-member's writer goes back to the pool.
func captureConnFor(ctx context.Context, path string, busyMS int64) (*heldCapture, error) {
	captureHeld.Lock()
	defer captureHeld.Unlock()
	held, ok := captureHeld.byPath[path]
	if ok && held.conn != nil {
		held.pinned = true
		return held, nil
	}
	conn, err := openCaptureConnection(ctx, path, busyMS)
	if err != nil {
		return nil, err
	}
	held = newHeldCapture(conn)
	held.pinned = true
	captureHeld.byPath[path] = held
	return held, nil
}

// borrowCapture is the held capture connection counted as a borrow: the caller
// puts rows into the change set over it and gives it back on Close.
//
// It is the held connection rather than a second one because the capture pool is
// a single connection wide and that connection is checked out for as long as the
// file is a member: a second open of the same pool waits for a slot the holder
// has no reason to release, so the walk would time out on a database nobody was
// fighting over. Sharing it also puts the walk behind the same gate the daemon's
// own writes take, so the two are serialised instead of racing for the write
// slot.
func borrowCapture(ctx context.Context, path string, busyMS int64) (*CaptureConn, error) {
	captureHeld.Lock()
	defer captureHeld.Unlock()
	held, ok := captureHeld.byPath[path]
	if !ok || held.conn == nil {
		conn, err := openCaptureConnection(ctx, path, busyMS)
		if err != nil {
			return nil, err
		}
		held = newHeldCapture(conn)
		captureHeld.byPath[path] = held
	}
	held.borrows++
	return &CaptureConn{held: held, path: path}, nil
}

// releaseBorrow gives a borrowed connection back, and closes it when nothing
// else is using it. A connection a handle writes through is left alone: that
// handle closes it with its own pool.
func releaseBorrow(path string, held *heldCapture) error {
	captureHeld.Lock()
	held.borrows--
	closeIt := held.borrows == 0 && !held.pinned && held.conn != nil
	if closeIt {
		delete(captureHeld.byPath, path)
	}
	captureHeld.Unlock()
	if !closeIt {
		return nil
	}
	return held.conn.Close()
}

// releaseHeldCapture closes a path's held capture connection, so it does not
// outlive the handle over the file it names.
func releaseHeldCapture(path string) error {
	captureHeld.Lock()
	held, ok := captureHeld.byPath[path]
	delete(captureHeld.byPath, path)
	captureHeld.Unlock()
	if !ok {
		return nil
	}
	// The gate token is taken out of the channel before the connection closes, so
	// a writer already over it finishes rather than running a statement on a
	// closed connection. The entry is gone from the map, so the token is not put
	// back.
	<-held.gate
	if err := held.conn.Close(); err != nil {
		return fmt.Errorf("db: %s: close the capture connection: %w", path, err)
	}
	return nil
}

// capturePoolFor is the member path's capture pool, built once.
func capturePoolFor(path string, busy int64) (*sql.DB, error) {
	capturePools.Lock()
	defer capturePools.Unlock()
	if pool, ok := capturePools.byPath[path]; ok {
		return pool, nil
	}
	base, err := newTursoConnector(path, busy)
	if err != nil {
		return nil, err
	}
	pool := sql.OpenDB(repairConnector{Connector: base})
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	capturePools.byPath[path] = pool
	return pool, nil
}

// Close releases the connection. It is discarded rather than returned to the
// pool, because the capture state is per connection: pooling it would hand a
// capturing connection to an unrelated caller and leave that caller's writes in
// a change set nobody asked for.
func (c *captureConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	// The discard is the same one the read-only query path makes, and for the
	// same reason: close the connection rather than pool a state that must not
	// outlive it.
	_ = c.conn.Raw(func(any) error { return driver.ErrBadConn })
	err := c.conn.Close()
	c.conn = nil
	if err != nil && !errors.Is(err, driver.ErrBadConn) {
		// Close reports "connection is already closed" once Raw discarded the
		// connection, which is the discard doing its job rather than a failure.
		return err
	}
	return nil
}

// closeCapturePool releases the member path's capture pool, so no pool outlives
// the handle over the file it names.
func closeCapturePool(path string) error {
	capturePools.Lock()
	pool, ok := capturePools.byPath[path]
	delete(capturePools.byPath, path)
	capturePools.Unlock()
	if !ok {
		return nil
	}
	if err := pool.Close(); err != nil {
		return fmt.Errorf("db: %s: close the capture pool: %w", path, err)
	}
	return nil
}

// Conn is the held connection, for the caller that has to write rows into the
// change set. It is nil on a zero captureConn, so a caller that got nothing
// from an open refuses rather than writing through a nil.
func (c *captureConn) Conn() *sql.Conn {
	if c == nil {
		return nil
	}
	return c.conn
}

// backfillColumns is one table's column names in declaration order, so a rewrite
// names the same columns the table has.
func backfillColumns(ctx context.Context, conn *sql.Conn, table string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("db: %s: read the columns: %w", table, mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("db: %s: read the columns: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: %s: read the columns: %w", table, err)
	}
	return columns, nil
}

// CaptureTables is every table the driver syncs from a member file: the file's
// own application tables, read out of its schema rather than from a fixed list,
// so a table this tree has not heard of is backfilled too. The driver's own
// tables are excluded by the same naming rule the driver's own sync engine uses
// -- everything under sqlite_, turso_ and __turso_ is the driver's, not the
// application's, and rewriting it would be rewriting the change set with itself.
func CaptureTables(ctx context.Context, conn *sql.Conn) ([]string, error) {
	const query = `SELECT name FROM sqlite_schema WHERE type = 'table' ` +
		`AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'turso_%' ` +
		`AND name NOT LIKE '\_\_turso\_%' ESCAPE '\' ORDER BY name`
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("db: read the member's tables: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("db: read the member's tables: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: read the member's tables: %w", err)
	}
	return tables, nil
}

// OpenCapture opens the capture connection over a file the sync engine has
// joined, so a caller outside this package can put rows in the change set. The
// pragma is asked on the connection this returns and nowhere else, which is what
// keeps it off the pool path every read takes.
//
// It is the path's held connection rather than one of its own: see borrowCapture.
func OpenCapture(ctx context.Context, path string, busyMS int64) (*CaptureConn, error) {
	return borrowCapture(ctx, path, busyMS)
}

// CaptureConn is the held capture connection as a caller outside this package
// sees it. Each of its calls takes the gate, so a walk over a file the daemon is
// writing to is serialised against those writes rather than racing them.
type CaptureConn struct {
	held *heldCapture
	path string
}

// Tables is the file's own application tables, the ones a backfill walks.
func (c *CaptureConn) Tables(ctx context.Context) ([]string, error) {
	conn, release, err := c.held.acquire(ctx)
	if err != nil {
		return nil, stepError(c.path, "the backfill's table list", err)
	}
	defer release()
	return CaptureTables(ctx, conn)
}

// Close gives the connection back to the path. It closes nothing a handle still
// writes through, so a walk that finished cannot take the daemon's writes with it.
func (c *CaptureConn) Close() error {
	if c == nil || c.held == nil {
		return nil
	}
	held := c.held
	c.held = nil
	return releaseBorrow(c.path, held)
}
