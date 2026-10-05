//go:build !unix

package client

import (
	"context"
	"errors"
	"net"
	"runtime"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// DriverName is the name the wire driver would register under; nothing is
// registered here.
const DriverName = "relevo-owner"

// SetDialer is a no-op off unix: there is no socket to dial.
func SetDialer(func(context.Context, string) (net.Conn, error)) {}

// SetHandshakeTimeout is a no-op off unix: there is no socket to dial.
func SetHandshakeTimeout(time.Duration) {}

// info is the handshake answer, unavailable off unix.
type info struct {
	Have     int
	Know     int
	Origin   string
	PID      int
	Conns    int
	HasLocal bool
}

// Info refuses: the owner protocol is a unix socket, and relevo serves the
// database over one only on Linux and macOS.
func Info(context.Context, string) (info, error) {
	return info{}, errors.New("relevo database dialling is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}

// SyncVerb refuses: there is no socket to send one over off unix.
func SyncVerb(context.Context, string, *wire.SyncVerb, []byte) (*wire.SyncResult, error) {
	return nil, errors.New("relevo sync verbs are not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}
