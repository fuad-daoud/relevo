//go:build unix

package owner

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// discardTimeout bounds how long cleanup waits for an interrupted statement to
// return before it rolls the pinned connection back anyway.
const discardTimeout = 2 * time.Second

// conn is one client connection. It pins one database connection for its life,
// runs one request at a time, and tracks that request so a cancel or a next can
// reach it from the read loop.
type conn struct {
	s  *Server
	nc net.Conn
	w  *wire.Conn

	mu     sync.Mutex
	pinned *sql.Conn
	cur    *request
	// inTx tracks whether this connection is inside a transaction, read from
	// the SQL already on the wire because relevo's Tx sends BEGIN/COMMIT as
	// plain statements rather than through database/sql's BeginTx.
	inTx bool
	// adHoc is the marker this connection's client sent in the handshake: only
	// such a connection may be refused while the owner reaps a runaway
	// statement, so every other verb keeps being served.
	adHoc bool
}

type request struct {
	id     int
	cancel context.CancelFunc
	next   chan struct{}
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newRequest(id int) (*request, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	return &request{
		id:     id,
		cancel: cancel,
		next:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}, ctx
}

func (r *request) signalNext() {
	select {
	case r.next <- struct{}{}:
	default:
	}
}

func (r *request) signalStop() {
	r.once.Do(func() { close(r.stop) })
}

func (c *conn) serve() error {
	if c.s.isClosed() {
		return c.refuse(wire.RefuseShuttingDown, "the owner is shutting down")
	}
	hello, err := c.readHello()
	if err != nil {
		return err
	}
	if hello.Proto != wire.Proto || hello.Version != wire.Version {
		return c.refuse(wire.RefuseWrongProto, "protocol mismatch")
	}
	c.adHoc = hello.AdHoc
	w := &wire.Welcome{
		Header:     wire.Header{Type: wire.TypeWelcome},
		Proto:      wire.Proto,
		MinClient:  wire.Version,
		Version:    wire.Version,
		SchemaHave: c.s.have,
		SchemaKnow: c.s.know,
		Origin:     c.s.origin,
		PID:        os.Getpid(),
		Conns:      c.s.connCount(),
	}
	if err := c.send(wire.KindWelcome, w, nil); err != nil {
		return err
	}
	return c.loop()
}

func (c *conn) loop() error {
	for {
		frame, err := c.w.Read()
		if err != nil {
			return err
		}
		kind, err := wire.Kind(frame)
		if err != nil {
			return err
		}
		switch kind {
		case wire.KindExec, wire.KindQuery:
			if err := c.start(kind, frame); err != nil {
				return err
			}
		case wire.KindNext:
			if r := c.current(); r != nil {
				r.signalNext()
			}
		case wire.KindCancel:
			if r := c.current(); r != nil {
				r.cancel()
			}
		case wire.KindClose:
			return nil
		default:
			return fmt.Errorf("owner: unexpected frame kind %d", kind)
		}
	}
}

func (c *conn) readHello() (*wire.Hello, error) {
	frame, err := c.w.Read()
	if err != nil {
		return nil, err
	}
	kind, err := wire.Kind(frame)
	if err != nil {
		return nil, err
	}
	if kind != wire.KindHello {
		return nil, fmt.Errorf("owner: expected hello, got frame kind %d", kind)
	}
	var h wire.Hello
	if _, err := wire.Decode(frame, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (c *conn) current() *request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

// transactionOpen reports whether this connection is inside a transaction,
// which is what Drain waits for before it drops the clients.
func (c *conn) transactionOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inTx
}

// recordTransaction moves this connection's transaction membership from the
// statement that just succeeded. A failed COMMIT leaves the flag set: SQLite
// keeps that transaction open, so the connection still counts as in one.
func (c *conn) recordTransaction(query string) {
	open, closeTx := txEffect(query)
	if !open && !closeTx {
		return
	}
	c.mu.Lock()
	c.inTx = open
	c.mu.Unlock()
}

// txEffect reads a statement's leading keyword for the transaction state it
// leaves behind. BEGIN (and BEGIN IMMEDIATE) opens; COMMIT, END and ROLLBACK
// close.
func txEffect(query string) (open, closeTx bool) {
	switch firstKeyword(query) {
	case "BEGIN":
		return true, false
	case "COMMIT", "END", "ROLLBACK":
		return false, true
	}
	return false, false
}

// firstKeyword returns the statement's first word, upper-cased, so BEGIN
// IMMEDIATE is seen as BEGIN and the keyword match is case-insensitive.
func firstKeyword(query string) string {
	query = strings.TrimLeft(query, " \t\r\n")
	if i := strings.IndexAny(query, " \t\r\n("); i >= 0 {
		return strings.ToUpper(query[:i])
	}
	return strings.ToUpper(query)
}

// start launches one request so the read loop can keep receiving cancel and
// next while the statement runs.
func (c *conn) start(kind byte, frame []byte) error {
	var m struct {
		wire.Header
		Query string `json:"query"`
	}
	raw, err := wire.Decode(frame, &m)
	if err != nil {
		return err
	}

	// An ad-hoc connection is refused while an abandoned statement is being
	// reaped: the owner cannot interrupt that statement, so an ad-hoc read
	// could not be bounded. A connection that did not mark itself ad-hoc --
	// every normal relevo verb -- is never refused here.
	if c.adHoc && c.s.reaping() {
		return c.refuse(wire.RefuseReaping, "the owner is ending a statement that will not stop; retry in a moment")
	}

	// A drain refuses a request on a connection with no open transaction: the
	// owner guarantees it was never executed, so the client may retry it. A
	// request inside an open transaction runs, so the transaction can commit.
	if c.s.isDraining() && !c.transactionOpen() {
		return c.refuse(wire.RefuseRestarting, "the owner is restarting")
	}

	r, ctx, err := c.claim(m.ID)
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			c.mu.Lock()
			if c.cur == r {
				c.cur = nil
			}
			c.mu.Unlock()
			close(r.done)
			r.cancel()
		}()
		c.run(ctx, r, kind, m.Query, raw)
	}()
	return nil
}

// claim takes the connection's single request slot. A finished request clears
// the slot from its own goroutine, so a client that sends its next request the
// moment it reads the answer waits for that clear rather than having its whole
// connection dropped as though it had pipelined two requests.
func (c *conn) claim(id int) (*request, context.Context, error) {
	for {
		c.mu.Lock()
		cur := c.cur
		if cur == nil {
			r, ctx := newRequest(id)
			c.cur = r
			c.mu.Unlock()
			return r, ctx, nil
		}
		c.mu.Unlock()

		select {
		case <-cur.done:
		case <-time.After(discardTimeout):
			return nil, nil, fmt.Errorf("owner: request %d already in flight", cur.id)
		}
	}
}

func (c *conn) run(ctx context.Context, r *request, kind byte, query string, raw []byte) {
	pinned, err := c.pin(ctx)
	if err != nil {
		c.sendError(r.id, err)
		return
	}
	args, err := decodeArgs(raw)
	if err != nil {
		c.sendError(r.id, err)
		return
	}
	if kind == wire.KindExec {
		c.exec(ctx, r, pinned, query, args)
		return
	}
	c.query(ctx, r, pinned, query, args)
}

// pin opens the client's own owner connection on first use, holding one of the
// server's connection slots for it. The slot is taken on the request's context,
// with no deadline of its own, so a client over the cap waits here -- the
// handshake never does -- and a disconnect releases the wait.
func (c *conn) pin(ctx context.Context) (*sql.Conn, error) {
	c.mu.Lock()
	if c.pinned != nil {
		pinned := c.pinned
		c.mu.Unlock()
		return pinned, nil
	}
	c.mu.Unlock()

	select {
	case c.s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	pinned, err := c.s.dbh.Conn(ctx)
	if err != nil {
		<-c.s.sem
		return nil, err
	}
	c.pinned = pinned
	return pinned, nil
}

func (c *conn) exec(ctx context.Context, r *request, pinned *sql.Conn, query string, args []any) {
	res, err := pinned.ExecContext(ctx, query, args...)
	if err != nil {
		c.sendError(r.id, err)
		return
	}
	c.recordTransaction(query)
	rows, _ := res.RowsAffected()
	id, _ := res.LastInsertId()
	_ = c.send(wire.KindDone, &wire.Done{
		Header:       wire.Header{Type: wire.TypeDone, ID: r.id},
		RowsAffected: rows,
		LastInsertID: id,
	}, nil)
}

func (c *conn) query(ctx context.Context, r *request, pinned *sql.Conn, query string, args []any) {
	rows, err := pinned.QueryContext(ctx, query, args...)
	if err != nil {
		c.sendError(r.id, err)
		return
	}
	c.recordTransaction(query)
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		c.sendError(r.id, err)
		return
	}

	stream := &rowStream{rows: rows, cols: cols}
	for first := true; ; first = false {
		body, n, more, err := stream.next(wire.BatchBudget)
		if err != nil {
			c.sendError(r.id, err)
			return
		}
		hdr := &wire.Rows{Header: wire.Header{Type: wire.TypeRows, ID: r.id}, Count: n, More: more}
		if first {
			hdr.Columns = cols
		}
		if err := c.send(wire.KindRows, hdr, body); err != nil {
			return
		}
		if !more {
			_ = c.send(wire.KindDone, &wire.Done{Header: wire.Header{Type: wire.TypeDone, ID: r.id}}, nil)
			return
		}
		select {
		case <-r.next:
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// rowStream reads one query's rows, one row ahead, so the sender knows whether
// the batch that just filled is the last one.
type rowStream struct {
	rows        *sql.Rows
	cols        []string
	pending     []any
	havePending bool
}

// next fills a batch up to budget and reports whether another row follows.
func (s *rowStream) next(budget int) ([]byte, int, bool, error) {
	batch := &wire.Builder{}
	n := 0
	for batch.Len() < budget {
		vals, ok, err := s.take()
		if err != nil {
			return nil, 0, false, err
		}
		if !ok {
			break
		}
		for _, v := range vals {
			if err := batch.Add(v); err != nil {
				return nil, 0, false, err
			}
		}
		n++
	}
	more, err := s.peek()
	if err != nil {
		return nil, 0, false, err
	}
	return batch.Bytes(), n, more, nil
}

func (s *rowStream) take() ([]any, bool, error) {
	if s.havePending {
		s.havePending = false
		return s.pending, true, nil
	}
	return s.read()
}

func (s *rowStream) peek() (bool, error) {
	if s.havePending {
		return true, nil
	}
	vals, ok, err := s.read()
	if err != nil || !ok {
		return false, err
	}
	s.pending, s.havePending = vals, true
	return true, nil
}

func (s *rowStream) read() ([]any, bool, error) {
	if !s.rows.Next() {
		return nil, false, s.rows.Err()
	}
	vals := make([]any, len(s.cols))
	ptrs := make([]any, len(s.cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := s.rows.Scan(ptrs...); err != nil {
		return nil, false, err
	}
	return vals, true, nil
}

// cleanup runs when the client goes away: it interrupts any running statement,
// rolls the pinned connection back whatever state it was in, discards it, and
// frees the connection slot that pin took.
func (c *conn) cleanup() {
	c.mu.Lock()
	r := c.cur
	c.cur = nil
	c.mu.Unlock()

	pinned := c.takePinned()
	if r != nil {
		r.cancel()
		r.signalStop()
		select {
		case <-r.done:
		case <-time.After(discardTimeout):
		}
		// A request that was still acquiring its slot when cleanup began can
		// pin a connection after the read above; take it so its slot is freed
		// with the rest.
		if p := c.takePinned(); p != nil {
			pinned = p
		}
	}
	if pinned != nil {
		finish := discardPinned(pinned)
		select {
		case <-finish:
		case <-time.After(discardTimeout):
			// The statement is still running and the engine will not
			// interrupt it. The slot returns anyway, so a statement that never
			// ends cannot wedge a pin slot; the server tracks it as the
			// abandoned statement so ad-hoc reads are refused and the daemon
			// can reap it.
			c.s.noteAbandoned(finish)
		}
		<-c.s.sem
	}
	_ = c.nc.Close()
}

// discardPinned best-effort rolls a pinned connection back and drops it, and
// returns a channel closed when that finished. The rollback runs on the
// connection's single-operation mutex, so a statement the engine will not
// interrupt parks it; the caller must return the slot regardless and may
// register the returned channel as an abandoned statement.
func discardPinned(pinned *sql.Conn) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = pinned.ExecContext(context.Background(), "ROLLBACK")
		_ = pinned.Raw(func(any) error { return driver.ErrBadConn })
		_ = pinned.Close()
	}()
	return done
}

// takePinned detaches the client's pinned connection, returning nil when there
// is none. A non-nil result owns the connection slot the pin took.
func (c *conn) takePinned() *sql.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	pinned := c.pinned
	c.pinned = nil
	return pinned
}

// shutdown drops the client connection, which makes serve return and cleanup
// roll the pinned connection back.
func (c *conn) shutdown() {
	_ = c.nc.SetReadDeadline(time.Now())
	_ = c.nc.Close()
}

func (c *conn) send(kind byte, v any, raw []byte) error {
	payload, err := wire.Encode(kind, v, raw)
	if err != nil {
		return err
	}
	return c.w.Write(payload)
}

func (c *conn) refuse(code, message string) error {
	return c.send(wire.KindRefuse, &wire.Refusal{
		Header:  wire.Header{Type: wire.TypeRefuse},
		Code:    code,
		Message: message,
	}, nil)
}

func (c *conn) sendError(id int, err error) {
	code, ext := c.s.errorCode(err)
	_ = c.send(wire.KindError, wire.NewError(id, code, ext, err.Error()), nil)
}

func decodeArgs(raw []byte) ([]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	cur := wire.NewCursor(raw)
	var out []any
	for {
		v, ok, err := cur.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, v)
	}
}
