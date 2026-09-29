//go:build linux

package owner

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the uid on the other end of a unix socket. The kernel fills
// SO_PEERCRED, so the value cannot be spoofed by the peer.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		cred *unix.Ucred
		serr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	return cred.Uid, nil
}
