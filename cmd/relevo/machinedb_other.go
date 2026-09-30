//go:build !unix

package main

import (
	"context"
	"fmt"
	"net"
)

// ownerRouteSupported reports that this platform has no owner socket, so the
// installer keeps the direct open.
func ownerRouteSupported() bool { return false }

// ownerSocket refuses: there is no socket path off unix.
func ownerSocket(string) (string, error) { return "", errOwnerUnavailable }

// ownerStarting reports that this platform never has a starting owner: there
// is no socket to connect to and no daemon lock to probe.
func ownerStarting(string, string) bool { return false }

// dialWithStart refuses: the owner protocol is a unix socket.
func dialWithStart(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("%w: no owner socket on this platform", errOwnerUnavailable)
}
