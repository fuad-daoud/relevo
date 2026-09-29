//go:build unix

package client

import (
	"context"
	"net"
)

// dialer is the process-wide dial hook. relevo installs one that starts the
// owner when the socket is missing, so the handshake and every pooled reconnect
// share the same route. Nil means a plain unix dial.
var dialer func(ctx context.Context, sock string) (net.Conn, error)

// SetDialer installs f as the dial hook. Passing nil restores the plain dial.
func SetDialer(f func(ctx context.Context, sock string) (net.Conn, error)) { dialer = f }
