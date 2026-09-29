//go:build unix

// Package owner serves one directly-opened database over a unix socket: it
// authenticates each peer, pins one database connection per client, streams
// rows, and rolls a client's work back when it disconnects.
package owner

import (
	"database/sql"
	"net"
	"os"
	"sync"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// maxConns caps simultaneous pinned connections. Only a request that needs a
// pinned connection takes a slot: the handshake never waits on the cap, and a
// client over the cap waits rather than being refused.
var maxConns = 64

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

	mu     sync.Mutex
	closed bool
	ln     net.Listener
	conns  map[*conn]struct{}
	// sem holds one slot per pinned connection; a request that needs one waits
	// on it inside pin, and cleanup returns it when that connection is
	// discarded. Idle handshaken connections never take a slot.
	sem chan struct{}
}

// New wraps a database handle, its schema versions and the installation id
// every scoped query needs.
func New(dbh *sql.DB, have, know int, origin string) *Server {
	return &Server{
		dbh:    dbh,
		have:   have,
		know:   know,
		origin: origin,
		uid:    ownerUID,
		conns:  make(map[*conn]struct{}),
		sem:    make(chan struct{}, maxConns),
	}
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
			if s.isClosed() {
				return nil
			}
			return err
		}
		go s.handle(nc)
	}
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
	s.addConn(c)
	defer s.removeConn(c)
	_ = c.serve()
	c.cleanup()
}

func newConn(s *Server, nc net.Conn) *conn {
	return &conn{s: s, nc: nc, w: wire.NewConn(nc)}
}

func (s *Server) addConn(c *conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeConn(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}
