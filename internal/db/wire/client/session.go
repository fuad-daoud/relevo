//go:build unix

package client

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// conn is one pinned owner connection behind one database/sql connection. The
// handshake runs once; every later request reuses the same owner connection.
type conn struct {
	nc     net.Conn
	w      *wire.Conn
	id     int
	have   int
	know   int
	origin string
	pid    int
	conns  int
	// adHoc is the marker every handshake this connection opens carries.
	adHoc bool
	// hasLocal is the owner's answer to whether it serves a machine-local file.
	hasLocal bool
	// scope is the file every request on this connection reaches, fixed by the
	// handshake: one connection never mixes the shared file and the local one.
	scope string
	// dead is set from the caller's goroutine and read from a cancel
	// AfterFunc, so it is atomic rather than a plain flag.
	dead atomic.Bool
}

// closeDrainTimeout bounds how long Close waits for the owner to finish the
// client's cleanup before closing the socket anyway.
const closeDrainTimeout = 2 * time.Second

// cancelGrace bounds how long a cancelled request waits for the owner's reply
// before the client gives up and returns the caller's own context error. An
// engine that cannot interrupt a running statement keeps the owner busy until
// the statement ends, so waiting for the reply would hold the caller for the
// whole statement; the grace still lets an owner that can interrupt answer.
const cancelGrace = 250 * time.Millisecond

func (c *conn) newID() int {
	c.id++
	return c.id
}

func (c *conn) handshake(ctx context.Context) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.nc.SetDeadline(dl)
		defer func() { _ = c.nc.SetDeadline(time.Time{}) }()
	}
	hello := &wire.Hello{
		Header:  wire.Header{Type: wire.TypeHello},
		Proto:   wire.Proto,
		Version: wire.Version,
		// schema_know is informational: the owner never acts on it, and the
		// client cannot read its own embedded maximum without importing the
		// package that owns the migrations.
		SchemaKnow: 0,
		AdHoc:      c.adHoc,
		Scope:      c.scope,
	}
	if err := c.send(wire.KindHello, hello, nil); err != nil {
		return err
	}
	frame, err := c.w.Read()
	if err != nil {
		return err
	}
	kind, err := wire.Kind(frame)
	if err != nil {
		return err
	}
	switch kind {
	case wire.KindWelcome:
		var m wire.Welcome
		if _, err := wire.Decode(frame, &m); err != nil {
			return err
		}
		c.have, c.know, c.origin = m.SchemaHave, m.SchemaKnow, m.Origin
		c.pid, c.conns = m.PID, m.Conns
		c.hasLocal = m.HasLocal
		return nil
	case wire.KindRefuse:
		var r wire.Refusal
		if _, err := wire.Decode(frame, &r); err != nil {
			return err
		}
		return &r
	default:
		return fmt.Errorf("client: unexpected frame kind %d during handshake", kind)
	}
}

func (c *conn) send(kind byte, v any, raw []byte) error {
	payload, err := wire.Encode(kind, v, raw)
	if err != nil {
		return err
	}
	return c.w.Write(payload)
}

// badConn marks the connection unusable and reports driver.ErrBadConn. It is
// used only before any byte of a request has reached the owner -- a dead
// connection, or a send that failed -- where a retry on a fresh connection
// cannot run a statement twice.
func (c *conn) badConn(err error) error {
	c.dead.Store(true)
	return fmt.Errorf("%w: %w", driver.ErrBadConn, err)
}

// connLost marks the connection unusable and reports a lost connection. It is
// used after a request has been sent: the owner may already have run the
// statement, so the error deliberately does not match driver.ErrBadConn and
// database/sql must not retry it.
func (c *conn) connLost(err error) error {
	c.dead.Store(true)
	return fmt.Errorf("%w: %w", wire.ErrConnLost, err)
}

// IsValid reports whether the connection may be reused. A dead connection fails
// this check, so the pool discards it instead of reusing a desynchronised
// stream.
func (c *conn) IsValid() bool { return !c.dead.Load() }

// ResetSession discards a dead connection before the pool hands it out again.
func (c *conn) ResetSession(context.Context) error {
	if c.dead.Load() {
		return driver.ErrBadConn
	}
	return nil
}

func (c *conn) sendCancel(id int) {
	if c.dead.Load() {
		return
	}
	_ = c.send(wire.KindCancel, &wire.Cancel{Header: wire.Header{Type: wire.TypeCancel, ID: id}}, nil)
}

// await reads one request's reply, racing it against the caller's context. When
// the context is cancelled it sends the cancel, waits up to cancelGrace for the
// owner's reply, then returns the caller's own context error and marks the
// connection dead. A stubborn statement only ever finishes on the owner, so the
// stream's state is no longer known; the dead flag makes the next use fail
// IsValid/ResetSession and database/sql discards the connection. The cancelled
// call itself returns the context error, never driver.ErrBadConn, so
// database/sql does not retry a statement that may already have run.
func (c *conn) await(ctx context.Context, id int) (byte, []byte, error) {
	type reply struct {
		kind  byte
		frame []byte
		err   error
	}
	ch := make(chan reply, 1)
	go func() {
		frame, err := c.w.Read()
		if err != nil {
			ch <- reply{err: err}
			return
		}
		kind, err := wire.Kind(frame)
		ch <- reply{kind: kind, frame: frame, err: err}
	}()

	select {
	case r := <-ch:
		if ctx.Err() != nil {
			c.dead.Store(true)
			return 0, nil, ctx.Err()
		}
		if r.err != nil {
			return 0, nil, c.connLost(r.err)
		}
		return r.kind, r.frame, nil
	case <-ctx.Done():
	}

	c.sendCancel(id)
	select {
	case <-ch:
	case <-time.After(cancelGrace):
	}
	c.dead.Store(true)
	return 0, nil, ctx.Err()
}

func decodeError(frame []byte) error {
	var m wire.Error
	if _, err := wire.Decode(frame, &m); err != nil {
		return err
	}
	return &m
}

// midRefuse turns a refuse frame received mid-request into the connection's
// error and retires the connection. A restarting refusal is retryable: the
// owner guarantees the request never ran, so it wraps driver.ErrBadConn (the
// refusal stays inspectable through the wrap) and database/sql retries it on a
// fresh connection. Any other refusal stays a plain error.
func (c *conn) midRefuse(frame []byte) error {
	var r wire.Refusal
	if _, err := wire.Decode(frame, &r); err != nil {
		return c.connLost(err)
	}
	c.dead.Store(true)
	if r.Code == wire.RefuseRestarting {
		return fmt.Errorf("%w: %w", driver.ErrBadConn, &r)
	}
	return &r
}

func encodeArgs(args []driver.NamedValue) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	var b wire.Builder
	for _, a := range args {
		if err := b.Add(a.Value); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func (c *conn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wire client: prepared statements are not supported")
}

func (c *conn) Close() error {
	if c.dead.Load() {
		return c.nc.Close()
	}
	c.dead.Store(true)
	// Tell the owner this connection is done and wait for it to close the
	// socket, so its rollback and discard of the pinned connection finish
	// before database/sql returns: whatever the database must write back to
	// the file happens while the caller is still inside Close.
	if err := c.send(wire.KindClose, &wire.Close{Header: wire.Header{Type: wire.TypeClose}}, nil); err != nil {
		return c.nc.Close()
	}
	_ = c.nc.SetReadDeadline(time.Now().Add(closeDrainTimeout))
	_, _ = c.w.Read()
	return c.nc.Close()
}

func (c *conn) Ping(context.Context) error {
	if c.dead.Load() {
		return driver.ErrBadConn
	}
	return nil
}

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *conn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if _, err := c.ExecContext(ctx, "BEGIN", nil); err != nil {
		return nil, err
	}
	return &wireTx{c: c}, nil
}

type wireTx struct{ c *conn }

func (t *wireTx) Commit() error {
	_, err := t.c.ExecContext(context.Background(), "COMMIT", nil)
	return err
}

func (t *wireTx) Rollback() error {
	_, err := t.c.ExecContext(context.Background(), "ROLLBACK", nil)
	return err
}

// wireResult carries the two numbers production callers read.
type wireResult struct {
	rows int64
	id   int64
}

func (r wireResult) LastInsertId() (int64, error) { return r.id, nil }
func (r wireResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.dead.Load() {
		return nil, driver.ErrBadConn
	}
	raw, err := encodeArgs(args)
	if err != nil {
		return nil, err
	}
	id := c.newID()
	if err := c.send(wire.KindExec, &wire.Exec{Header: wire.Header{Type: wire.TypeExec, ID: id}, Query: query}, raw); err != nil {
		return nil, c.badConn(err)
	}

	kind, frame, err := c.await(ctx, id)
	if err != nil {
		return nil, err
	}
	switch kind {
	case wire.KindDone:
		var m wire.Done
		if _, err := wire.Decode(frame, &m); err != nil {
			return nil, c.connLost(err)
		}
		return wireResult{rows: m.RowsAffected, id: m.LastInsertID}, nil
	case wire.KindError:
		return nil, decodeError(frame)
	case wire.KindRefuse:
		return nil, c.midRefuse(frame)
	default:
		return nil, c.connLost(fmt.Errorf("client: unexpected frame kind %d for exec", kind))
	}
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.dead.Load() {
		return nil, driver.ErrBadConn
	}
	raw, err := encodeArgs(args)
	if err != nil {
		return nil, err
	}
	id := c.newID()
	if err := c.send(wire.KindQuery, &wire.Query{Header: wire.Header{Type: wire.TypeQuery, ID: id}, Query: query}, raw); err != nil {
		return nil, c.badConn(err)
	}

	kind, frame, err := c.await(ctx, id)
	if err != nil {
		return nil, err
	}
	switch kind {
	case wire.KindRows:
		// Rows stream under their own cancel: a caller that closes early or
		// whose context ends mid-stream leaves the owner to roll the pinned
		// connection back.
		stop := context.AfterFunc(ctx, func() { c.sendCancel(id) })
		r := &rows{c: c, id: id, ctx: ctx, stop: stop}
		if err := r.fill(frame); err != nil {
			stop()
			return nil, err
		}
		return r, nil
	case wire.KindError:
		return nil, decodeError(frame)
	case wire.KindDone:
		return &emptyRows{}, nil
	case wire.KindRefuse:
		return nil, c.midRefuse(frame)
	default:
		return nil, c.connLost(fmt.Errorf("client: unexpected frame kind %d for query", kind))
	}
}

// SyncVerb sends one sync verb to the owner and returns its answer.
//
// It is a request of its own rather than an Exec, because the owner runs the
// verb against handles it already holds: there is no statement here to pin a
// connection for, and a pinned connection would hold a pool slot the request
// never uses. The token travels in the frame's raw tail and is handed straight
// to the owner's hook -- it is never formatted into an error, never logged and
// never sent back, and the only token field on the answer is a bool.
//
// A refusal arrives as the result itself rather than as an error, so a caller
// classifies a failed verb by code. Only a lost connection is an error here.
func (c *conn) SyncVerb(ctx context.Context, verb *wire.SyncVerb, token []byte) (*wire.SyncResult, error) {
	if c.dead.Load() {
		return nil, driver.ErrBadConn
	}
	id := c.newID()
	if err := c.send(wire.KindSyncVerb, verb, token); err != nil {
		return nil, c.badConn(err)
	}

	kind, frame, err := c.await(ctx, id)
	if err != nil {
		// A wait the caller's own context ended, after the frame was written,
		// is marked as such: the request was delivered, so a caller must not
		// read it as one that never arrived. Everything before the write -- a
		// dial, a handshake, a failed send -- stays a plain error.
		if cerr := ctx.Err(); cerr != nil && errors.Is(err, cerr) {
			return nil, fmt.Errorf("sync verb %s: %w: %w", verb.Verb, ErrAwaitingReply, cerr)
		}
		return nil, err
	}
	switch kind {
	case wire.KindSyncResult:
		var m wire.SyncResult
		if _, err := wire.Decode(frame, &m); err != nil {
			return nil, c.connLost(err)
		}
		return &m, nil
	case wire.KindError:
		return nil, decodeError(frame)
	case wire.KindRefuse:
		return nil, c.midRefuse(frame)
	default:
		return nil, c.connLost(fmt.Errorf("client: unexpected frame kind %d for a sync verb", kind))
	}
}

// emptyRows is a result set with no columns, which database/sql accepts.
type emptyRows struct{}

func (e *emptyRows) Columns() []string         { return nil }
func (e *emptyRows) Close() error              { return nil }
func (e *emptyRows) Next([]driver.Value) error { return io.EOF }

// rows streams one query's batches. A batch carries more=true when another
// batch follows; otherwise a done frame follows the batch immediately, so
// Close can drain the stream without a next.
type rows struct {
	c  *conn
	id int
	// ctx is the query's context, kept so every batch read can race it through
	// await: a cancelled stream must return to the caller within the grace
	// rather than wait for the owner to finish the statement.
	ctx  context.Context
	stop func() bool
	cols []string
	buf  []any
	pos  int
	more bool
	done bool
}

func (r *rows) Columns() []string { return r.cols }

func (r *rows) Close() error {
	r.stop()
	if r.done {
		return nil
	}
	if !r.more {
		// The server sent the terminating done after the last batch; consume
		// it so the connection stays in sync.
		return r.awaitDone()
	}
	// Abandoned mid-stream: the owner rolls its pinned connection back and
	// discards it, and this driver connection goes with it.
	r.c.dead.Store(true)
	_ = r.c.send(wire.KindClose, &wire.Close{Header: wire.Header{Type: wire.TypeClose, ID: r.id}}, nil)
	return driver.ErrBadConn
}

func (r *rows) Next(dest []driver.Value) error {
	// A statement whose result carries no columns -- a bare PRAGMA assignment,
	// say -- can carry no rows either, but database/sql still calls Next with a
	// zero-length destination. Without this the loop below neither advances nor
	// ends, so the caller spins on the same empty row forever: Close drains the
	// done frame the owner already sent after the empty batch, so the stream
	// stays in sync.
	if len(dest) == 0 {
		return io.EOF
	}
	for r.pos+len(dest) > len(r.buf) {
		if r.done {
			return io.EOF
		}
		var err error
		if r.more {
			err = r.requestNext()
		} else {
			err = r.awaitDone()
		}
		if err != nil {
			return err
		}
	}
	for i := range dest {
		dest[i] = r.buf[r.pos+i]
	}
	r.pos += len(dest)
	return nil
}

func (r *rows) requestNext() error {
	if err := r.c.send(wire.KindNext, &wire.Next{Header: wire.Header{Type: wire.TypeNext, ID: r.id}}, nil); err != nil {
		return r.c.badConn(err)
	}
	kind, frame, err := r.c.await(r.ctx, r.id)
	if err != nil {
		return err
	}
	switch kind {
	case wire.KindRows:
		return r.fill(frame)
	case wire.KindDone:
		r.done = true
		return nil
	case wire.KindError:
		return decodeError(frame)
	case wire.KindRefuse:
		return r.c.midRefuse(frame)
	default:
		return r.c.connLost(fmt.Errorf("client: unexpected frame kind %d for query", kind))
	}
}

// awaitDone reads the done frame the server sent after a final batch.
func (r *rows) awaitDone() error {
	kind, frame, err := r.c.await(r.ctx, r.id)
	if err != nil {
		return err
	}
	switch kind {
	case wire.KindDone:
		r.done = true
		return nil
	case wire.KindError:
		return decodeError(frame)
	case wire.KindRefuse:
		return r.c.midRefuse(frame)
	default:
		return r.c.connLost(fmt.Errorf("client: expected done, got frame kind %d", kind))
	}
}

func (r *rows) fill(frame []byte) error {
	var m wire.Rows
	raw, err := wire.Decode(frame, &m)
	if err != nil {
		return err
	}
	if m.Columns != nil {
		r.cols = m.Columns
	}
	buf := make([]any, 0, m.Count*len(r.cols))
	cur := wire.NewCursor(raw)
	for {
		v, ok, err := cur.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		buf = append(buf, v)
	}
	r.buf = buf
	r.pos = 0
	r.more = m.More
	return nil
}
