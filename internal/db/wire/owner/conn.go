//go:build unix

package owner

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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

// conn serves one client connection, runs one request at a time, and tracks
// that request so a cancel or a next can reach it from the read loop. A
// database connection is pinned only while a statement or transaction needs
// it: an idle connection holds no slot, so the cap binds real work.
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
	// local is whether this connection asked for the machine-local file rather
	// than the shared one. It is read from the handshake once and never changes
	// for the life of the connection, so every request on it reaches the same
	// file: a connection cannot start on one file and continue on the other.
	local bool
	// session records that a statement here changed per-connection engine
	// state (query_only, an attached file, a temp object). Such a database
	// connection must never reach another client, so it stays pinned until
	// this client goes and cleanup discards it.
	session bool
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
	if err := c.claimScope(hello.Scope); err != nil {
		return err
	}
	w := &wire.Welcome{
		Header:     wire.Header{Type: wire.TypeWelcome},
		Proto:      wire.Proto,
		MinClient:  wire.Version,
		Version:    wire.Version,
		SchemaHave: c.s.have,
		SchemaKnow: c.s.know,
		Origin:     c.s.origin,
		PID:        os.Getpid(),
		Conns:      c.s.ConnCount(),
		Pinned:     len(c.s.sem),
		HasLocal:   c.s.hasLocal(),
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
		case wire.KindSyncVerb:
			if err := c.startVerb(frame); err != nil {
				return err
			}
		case wire.KindSyncFreshen:
			c.freshen()
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
	if err != nil && wire.IsShortReadWAL(err) {
		// A poisoned pool refuses even the pin: its per-connection setup
		// reads the stale WAL. Swap it here so the next statement -- this
		// client's retry, or the next client's -- lands on the fresh pool;
		// this statement still fails with its original error, and a failed
		// pin holds no connection to discard first.
		c.s.recoverStale(c.local, err)
	}
	if err != nil {
		// A pin that timed out already names its refusal: answer it rather
		// than reporting an error, so the client sees the busy code.
		var busy *slotBusyError
		if errors.As(err, &busy) {
			// The connection is unusable when this send fails, and run has
			// nothing else to answer on, so the error is dropped.
			_ = c.refuse(wire.RefuseBusy, busy.Error())
			return
		}
		c.sendError(r.id, err)
		return
	}
	// The statement below answers and returns; the release runs after either,
	// so every path -- ack, error, or an abandoned stream -- passes through it.
	defer c.release()
	if changesSession(query) {
		c.mu.Lock()
		c.session = true
		c.mu.Unlock()
	}
	args, err := decodeArgs(raw)
	if err != nil {
		c.sendError(r.id, err)
		return
	}
	c.s.noteQuery(kind, c.adHoc, c.local, query)
	if kind == wire.KindExec {
		c.exec(ctx, r, pinned, query, args)
		return
	}
	c.query(ctx, r, pinned, query, args)
}

// changesSession reports whether query can change state that lives on the
// engine connection rather than in the database: any PRAGMA (query_only among
// them), ATTACH, DETACH, or a TEMP object. It errs towards true, since the
// cost is one slot held until the client leaves, while the miss is a pooled
// connection that refuses the next client's writes.
func changesSession(query string) bool {
	switch firstKeyword(query) {
	case "PRAGMA", "ATTACH", "DETACH":
		return true
	case "CREATE":
		rest := strings.Fields(strings.ToUpper(query))
		return len(rest) > 1 && (rest[1] == "TEMP" || rest[1] == "TEMPORARY")
	}
	return false
}

func (c *conn) exec(ctx context.Context, r *request, pinned *sql.Conn, query string, args []any) {
	res, err := pinned.ExecContext(ctx, query, args...)
	if err != nil && wire.IsShortReadWAL(err) {
		// A stale WAL poisons the whole pool, so swap it even though this
		// write is never retried: the next statement needs the fresh pool.
		// The pinned connection is dropped first, so no stale connection
		// stays out while the hook opens and probes its replacement. Any
		// other error leaves the connection alone: a failed statement inside
		// a transaction must roll back on the connection that ran it.
		c.dropPinned()
		c.s.recoverStale(c.local, err)
	}
	if err != nil {
		c.sendError(r.id, err)
		return
	}
	// Bracket the ack with the membership change: a BEGIN records before the
	// Done so a drain already sees the transaction, and a COMMIT, END or
	// ROLLBACK clears only after the Done so a drain that starts while the ack
	// is in flight waits for it rather than dropping the connection with the
	// answer unsent.
	open, closeTx := txEffect(query)
	if open {
		c.recordTransaction(query)
	}
	rows, _ := res.RowsAffected()
	id, _ := res.LastInsertId()
	_ = c.send(wire.KindDone, &wire.Done{
		Header:       wire.Header{Type: wire.TypeDone, ID: r.id},
		RowsAffected: rows,
		LastInsertID: id,
	}, nil)
	if closeTx {
		c.recordTransaction(query)
	}
}

func (c *conn) query(ctx context.Context, r *request, pinned *sql.Conn, query string, args []any) {
	rows, err := pinned.QueryContext(ctx, query, args...)
	// retried reports whether the statement already ran on the recovered
	// pool: one retry ever, never two, so a second stale failure answers with
	// its own error rather than swapping pools forever.
	retried := false
	if err != nil {
		if fresh, ok := c.retryStale(ctx, query, err); ok {
			rows, err = fresh.QueryContext(ctx, query, args...)
			retried = true
		}
		if err != nil {
			c.sendError(r.id, err)
			return
		}
	}
	c.recordTransaction(query)
	// The first-batch retry below closes and replaces rows; the guard keeps
	// a failed replacement from closing nothing twice.
	defer func() {
		if rows != nil {
			_ = rows.Close()
		}
	}()

	cols, err := rows.Columns()
	if err != nil {
		c.sendError(r.id, err)
		return
	}

	stream := &rowStream{rows: rows, cols: cols, ceiling: c.adHocCeiling()}
	for first := true; ; first = false {
		body, n, more, err := stream.next(wire.BatchBudget)
		if err != nil {
			// A first batch that fails met the stale pool before answering
			// anything, so the read is still retryable exactly once on the
			// healed pool. A later batch already sent rows the client keeps.
			// The rows close before the retry drops the connection: marking
			// a connection bad while its rows are still open deadlocks the
			// driver against the rows' own cleanup.
			if first && !retried {
				_ = rows.Close()
				rows = nil
				if fresh, ok := c.retryStale(ctx, query, err); ok {
					if newRows, rerr := fresh.QueryContext(ctx, query, args...); rerr == nil {
						if newCols, rerr := newRows.Columns(); rerr == nil {
							rows, cols = newRows, newCols
							stream = &rowStream{rows: rows, cols: cols, ceiling: c.adHocCeiling()}
							retried = true
							continue
						}
						_ = newRows.Close()
						err = rerr
					} else {
						err = rerr
					}
				}
			}
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

// cleanup runs when the client goes away: it interrupts any running statement,
// rolls the pinned connection back whatever state it was in, discards it, and
// frees the connection slot that pin took. When the last statement already
// released the connection there is nothing to discard and the slot is already
// free.
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
	// The count drops before the socket closes: a client that has seen the
	// close may handshake again at once, and must not find itself still live.
	c.s.removeConn(c)
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
