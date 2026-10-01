//go:build unix

// Package owner serves one directly-opened database over a unix socket: it
// authenticates each peer, pins one database connection per client, streams
// rows, and rolls a client's work back when it disconnects.
package owner

import (
	"context"
	"database/sql"
	"net"
	"os"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// maxConns caps simultaneous pinned connections. Only a request that needs a
// pinned connection takes a slot: the handshake never waits on the cap, and a
// client over the cap waits rather than being refused.
var maxConns = 64

// maxLiveConns caps every connection the owner tracks at once -- handshaken,
// idle, pinned, or still waiting for hello -- not only the pinned slots
// maxConns bounds. It is 4x the pinned cap, so a burst of real work still
// fits; a connection accepted over it is dropped rather than served.
var maxLiveConns = 256

// ownerUID is the uid every peer must match. It is read once by New, so a test
// can require a different one before starting a server and watch the
// connection drop.
var ownerUID = os.Getuid()

// Server serves one database handle. Construct it with New and run it with
// Serve; Close stops it and drops every client.
type Server struct {
	dbh    *sql.DB
	have   int
	know   int
	origin string
	uid    int

	// codeOf resolves a driver error's sqlite result code. It is nil when the
	// server was built without one: the wire's own CodeOf and ExtendedCodeOf
	// then carry the code, which is what a modernc-backed owner needs.
	codeOf func(error) (int, int, bool)

	mu     sync.Mutex
	closed bool
	// draining is set for the life of a drain: the accept loop stops, a request
	// on a connection with no open transaction is refused, and a connection
	// already inside one is let through so it can commit.
	draining bool
	ln       net.Listener
	conns    map[*conn]struct{}
	// maxLive is the live-connection bound read once in New, so a test can
	// shrink the package var before the accept goroutines start.
	maxLive int
	// sem holds one slot per pinned connection; a request that needs one waits
	// on it inside pin, and cleanup returns it when that connection is
	// discarded. Idle handshaken connections never take a slot.
	sem chan struct{}

	// OnAbandoned, when set, runs once when an abandoned statement has not
	// finished within reapGrace. The daemon uses it to ask for a re-exec, the
	// only way to end a statement the engine cannot interrupt.
	OnAbandoned func()

	// reapMu guards the abandoned-statement registration: the one finish
	// channel the server tracks and whether its hook already ran.
	reapMu      sync.Mutex
	reapPending <-chan struct{}
	reapFired   bool
}

// New wraps a database handle, its schema versions and the installation id
// every scoped query needs. codeOf maps a driver error onto a SQLite result
// code; nil falls back to the wire's own CodeOf and ExtendedCodeOf.
func New(dbh *sql.DB, have, know int, origin string, codeOf func(error) (int, int, bool)) *Server {
	return &Server{
		dbh:     dbh,
		have:    have,
		know:    know,
		origin:  origin,
		uid:     ownerUID,
		codeOf:  codeOf,
		conns:   make(map[*conn]struct{}),
		maxLive: maxLiveConns,
		sem:     make(chan struct{}, maxConns),
	}
}

// errorCode resolves err's SQLite result code and extended code, preferring the
// server's resolver when one was installed. A Turso error wraps a sentinel and
// exposes no Code method, so only the resolver can name it; a modernc error
// carries its code and needs no help.
func (s *Server) errorCode(err error) (int, int) {
	if s.codeOf != nil {
		if code, ext, ok := s.codeOf(err); ok {
			return code, ext
		}
	}
	code, _ := wire.CodeOf(err)
	return code, wire.ExtendedCodeOf(err)
}

// Serve accepts connections until the server is closed. Each accepted
// connection is handled in its own goroutine.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	s.ln = l
	s.mu.Unlock()

	for {
		nc, err := l.Accept()
		if err != nil {
			// A drain unblocks Accept without closing the listener: the caller
			// keeps it open to hand the fd to the next image.
			if s.isClosed() || s.isDraining() {
				return nil
			}
			return err
		}
		go s.handle(nc)
	}
}

// drainDeadline bounds how long Drain lets an open transaction finish before it
// drops the clients.
const drainDeadline = 5 * time.Second

// drainPoll is how often Drain re-checks whether a connection still holds an
// open transaction.
const drainPoll = 20 * time.Millisecond

// Drain stops admitting new work and lets the work already inside a transaction
// finish before it drops the clients. It never closes the listener: the caller
// may hand it to the next image. A transaction that outlasts the deadline is
// cut off at its next statement when the clients are dropped.
func (s *Server) Drain(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, drainDeadline)
	defer cancel()

	s.beginDrain()
	s.quiet()
	s.waitTransactions(ctx)
	s.dropClients()
	return nil
}

// beginDrain marks the server draining under the lock, so every later request
// on a connection with no open transaction is refused rather than run.
func (s *Server) beginDrain() {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
}

// quiet stops the accept loop while keeping the listener open: the deadline
// unblocks Accept, and Serve returns without closing the listener when it sees
// the drain flag. A connection that arrives after this sits in the kernel
// backlog for the next image to accept.
func (s *Server) quiet() {
	s.mu.Lock()
	l := s.ln
	s.mu.Unlock()
	if dl, ok := l.(interface{ SetDeadline(time.Time) error }); ok {
		_ = dl.SetDeadline(time.Now())
	}
}

// waitTransactions returns when no connection still holds an open transaction,
// or when ctx is done.
func (s *Server) waitTransactions(ctx context.Context) {
	for {
		if !s.anyInTx() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(drainPoll):
		}
	}
}

// anyInTx reports whether any live connection is still inside a transaction.
func (s *Server) anyInTx() bool {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		if c.transactionOpen() {
			return true
		}
	}
	return false
}

// dropClients closes every live client connection. Each connection's cleanup
// rolls its pinned connection back; the listener is untouched.
func (s *Server) dropClients() {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		c.shutdown()
	}
}

func (s *Server) isDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// Close stops the server: it closes the listener and every live client. A
// later connection is refused while the server is shutting down.
func (s *Server) Close() error {
	s.mu.Lock()
	l := s.ln
	s.closed = true
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		c.shutdown()
	}
	if l != nil {
		return l.Close()
	}
	return nil
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// connCount is the number of live connections, read for the welcome a client
// sees so its answer names the owner's real load.
func (s *Server) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) handle(nc net.Conn) {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		_ = nc.Close()
		return
	}
	if uid, err := peerUID(uc); err != nil || int(uid) != s.uid {
		_ = nc.Close()
		return
	}

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		c := newConn(s, nc)
		_ = c.refuse(wire.RefuseShuttingDown, "the owner is shutting down")
		_ = nc.Close()
		return
	}

	c := newConn(s, nc)
	if !s.addConn(c) {
		// Over the live bound the connection is dropped without a frame; an
		// existing connection keeps working and the client retries.
		_ = nc.Close()
		return
	}
	defer s.removeConn(c)
	_ = c.serve()
	c.cleanup()
}

func newConn(s *Server, nc net.Conn) *conn {
	return &conn{s: s, nc: nc, w: wire.NewConn(nc)}
}

// addConn records a live connection, refusing it when the bound is already
// reached. The check and the insertion share one critical section, so
// concurrent accepts cannot both pass it. It reports whether the connection was
// admitted; the caller must close a refused connection without any frame.
func (s *Server) addConn(c *conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) >= s.maxLive {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) removeConn(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}
