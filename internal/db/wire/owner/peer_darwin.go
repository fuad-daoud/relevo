//go:build darwin

package owner

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the uid on the other end of a unix socket. The kernel fills
// the xucred for LOCAL_PEERCRED, so the value cannot be spoofed by the peer.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		cred *unix.Xucred
		serr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	return cred.Uid, nil
}
