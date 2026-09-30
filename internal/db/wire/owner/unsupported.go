//go:build !unix

package owner

import (
	"database/sql"
	"errors"
	"net"
	"runtime"
)

// Server has no implementation off unix: relevo serves the database over a
// unix socket only on Linux and macOS, and this file keeps the tree compiling
// elsewhere by refusing.
type Server struct{}

// New returns a server that refuses to serve.
func New(*sql.DB, int, int, string, func(error) (int, int, bool)) *Server { return &Server{} }

// Serve refuses: there is no socket transport on this platform.
func (*Server) Serve(net.Listener) error {
	return errors.New("relevo database ownership is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}

// Close does nothing.
func (*Server) Close() error { return nil }
