//go:build !unix

package owner

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
)

func socketUnsupported() error {
	return errors.New("relevo database ownership is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}

// SocketPath refuses: there is no socket path off unix.
func SocketPath(string) (string, error) { return "", socketUnsupported() }

// Listen refuses: there is no socket transport off unix.
func Listen(string) (net.Listener, error) { return nil, socketUnsupported() }

// Adopt refuses: there is no socket to adopt off unix.
func Adopt(int) (net.Listener, error) { return nil, socketUnsupported() }

// Inherit refuses: there is no listener to hand over off unix.
func Inherit(net.Listener) (*os.File, error) { return nil, socketUnsupported() }

// Drain is a no-op off unix: the stub server has no listener to drain.
func (*Server) Drain(context.Context) error { return nil }
