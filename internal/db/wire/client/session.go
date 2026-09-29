//go:build unix

package client

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
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
	dead   bool
}

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

// ioErr marks the connection unusable and reports driver.ErrBadConn, so
// database/sql discards it rather than reusing a desynchronised stream.
func (c *conn) ioErr(err error) error {
	c.dead = true
	return fmt.Errorf("%w: %w", driver.ErrBadConn, err)
}

func (c *conn) sendCancel(id int) {
	if c.dead {
		return
	}
	_ = c.send(wire.KindCancel, &wire.Cancel{Header: wire.Header{Type: wire.TypeCancel, ID: id}}, nil)
}

// readFrame reads and returns one frame's kind and raw payload.
func (c *conn) readFrame() (byte, []byte, error) {
	frame, err := c.w.Read()
	if err != nil {
		return 0, nil, c.ioErr(err)
	}
	kind, err := wire.Kind(frame)
	if err != nil {
		return 0, nil, c.ioErr(err)
	}
	return kind, frame, nil
}

func decodeError(frame []byte) error {
	var m wire.Error
	if _, err := wire.Decode(frame, &m); err != nil {
		return err
	}
	return &m
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
	c.dead = true
	return c.nc.Close()
}

func (c *conn) Ping(context.Context) error {
	if c.dead {
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
	if c.dead {
		return nil, driver.ErrBadConn
	}
	raw, err := encodeArgs(args)
	if err != nil {
		return nil, err
	}
	id := c.newID()
	if err := c.send(wire.KindExec, &wire.Exec{Header: wire.Header{Type: wire.TypeExec, ID: id}, Query: query}, raw); err != nil {
		return nil, c.ioErr(err)
	}
	stop := context.AfterFunc(ctx, func() { c.sendCancel(id) })
	defer stop()

	kind, frame, err := c.readFrame()
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// The owner has been told to interrupt; the stream's state is no
		// longer known, so the connection is abandoned and database/sql
		// resumes the caller's context error.
		c.dead = true
		return nil, driver.ErrBadConn
	}
	switch kind {
	case wire.KindDone:
		var m wire.Done
		if _, err := wire.Decode(frame, &m); err != nil {
			return nil, c.ioErr(err)
		}
		return wireResult{rows: m.RowsAffected, id: m.LastInsertID}, nil
	case wire.KindError:
		return nil, decodeError(frame)
	default:
		c.dead = true
		return nil, fmt.Errorf("client: unexpected frame kind %d for exec: %w", kind, driver.ErrBadConn)
	}
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.dead {
		return nil, driver.ErrBadConn
	}
	raw, err := encodeArgs(args)
	if err != nil {
		return nil, err
	}
	id := c.newID()
	if err := c.send(wire.KindQuery, &wire.Query{Header: wire.Header{Type: wire.TypeQuery, ID: id}, Query: query}, raw); err != nil {
		return nil, c.ioErr(err)
	}
	stop := context.AfterFunc(ctx, func() { c.sendCancel(id) })

	kind, frame, err := c.readFrame()
	if err != nil {
		stop()
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		stop()
		c.dead = true
		return nil, driver.ErrBadConn
	}
	switch kind {
	case wire.KindRows:
		r := &rows{c: c, id: id, stop: stop}
		if err := r.fill(frame); err != nil {
			stop()
			return nil, err
		}
		return r, nil
	case wire.KindError:
		stop()
		return nil, decodeError(frame)
	case wire.KindDone:
		stop()
		return &emptyRows{}, nil
	default:
		stop()
		c.dead = true
		return nil, fmt.Errorf("client: unexpected frame kind %d for query: %w", kind, driver.ErrBadConn)
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
	c    *conn
	id   int
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
	r.c.dead = true
	_ = r.c.send(wire.KindClose, &wire.Close{Header: wire.Header{Type: wire.TypeClose, ID: r.id}}, nil)
	return driver.ErrBadConn
}

func (r *rows) Next(dest []driver.Value) error {
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
		return r.c.ioErr(err)
	}
	kind, frame, err := r.c.readFrame()
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
	default:
		r.c.dead = true
		return fmt.Errorf("client: unexpected frame kind %d for query: %w", kind, driver.ErrBadConn)
	}
}

// awaitDone reads the done frame the server sent after a final batch.
func (r *rows) awaitDone() error {
	kind, frame, err := r.c.readFrame()
	if err != nil {
		return err
	}
	switch kind {
	case wire.KindDone:
		r.done = true
		return nil
	case wire.KindError:
		return decodeError(frame)
	default:
		r.c.dead = true
		return fmt.Errorf("client: expected done, got frame kind %d: %w", kind, driver.ErrBadConn)
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
