//go:build !unix

package owner

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"runtime"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// Server has no implementation off unix: relevo serves the database over a
// unix socket only on Linux and macOS, and this file keeps the tree compiling
// elsewhere by refusing.
type Server struct {
	// OnSyncVerb is the hook the daemon installs on every platform. A server
	// that never serves never calls it.
	OnSyncVerb func(ctx context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult
}

// New returns a server that refuses to serve.
func New(*sql.DB, int, int, string, func(error) (int, int, bool)) *Server { return &Server{} }

// Serve refuses: there is no socket transport on this platform.
func (*Server) Serve(net.Listener) error {
	return errors.New("relevo database ownership is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}

// Close does nothing.
func (*Server) Close() error { return nil }

// ServeLocal does nothing off unix: the server never serves anything here.
func (s *Server) ServeLocal(*sql.DB) *Server { return s }

// ConnCount is always 0 off unix: there is no server and no socket a client
// could hold open.
func (*Server) ConnCount() int { return 0 }
