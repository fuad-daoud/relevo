//go:build !unix

package client

import (
	"context"
	"errors"
	"runtime"
)

// DriverName is the name the wire driver would register under; nothing is
// registered here.
const DriverName = "relevo-owner"

// info is the handshake answer, unavailable off unix.
type info struct {
	Have   int
	Know   int
	Origin string
	PID    int
	Conns  int
}

// Info refuses: the owner protocol is a unix socket, and relevo serves the
// database over one only on Linux and macOS.
func Info(context.Context, string) (info, error) {
	return info{}, errors.New("relevo database dialling is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}
