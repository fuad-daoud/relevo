//go:build unix

package owner

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// socketName is the file under the state root that carries the owner protocol.
const socketName = "relevo.sock"

// SocketPath builds the owner's socket path under root. A path that cannot fit
// the platform's sun_path can never bind, so it is refused here, naming the
// path and the limit before the daemon opens or migrates anything.
func SocketPath(root string) (string, error) {
	path := filepath.Join(root, socketName)
	if len(path) >= sunPathLimit {
		return "", fmt.Errorf("owner: socket path %q is %d bytes, over the %d-byte sun_path limit", path, len(path), sunPathLimit)
	}
	return path, nil
}

// Listen binds the owner's socket under root. Any file already at the path is
// removed first: the caller holds the daemon lock, so no live owner can exist
// and the file can only be a stale socket a dead owner left. The socket is mode
// 0600 inside the root.
func Listen(root string) (net.Listener, error) {
	path, err := SocketPath(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("owner: create %s: %w", root, err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("owner: remove stale socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("owner: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("owner: chmod %s: %w", path, err)
	}
	return ln, nil
}

// Adopt takes the listener an earlier image bound and passed as a file
// descriptor. It never unlinks and never rebinds, so connections queued in the
// backlog are served by this image instead of being reset.
//
// It takes ownership of fd: net.FileListener dups the descriptor, so the
// *os.File Adopt builds is closed as soon as the dup exists. Leaving it open
// would leave a second file whose finalizer closes the same fd number later,
// after the caller had closed or reused it.
func Adopt(fd int) (net.Listener, error) {
	f := os.NewFile(uintptr(fd), socketName)
	if f == nil {
		return nil, fmt.Errorf("owner: invalid listen fd %d", fd)
	}
	ln, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("owner: adopt fd %d: %w", fd, err)
	}
	return ln, nil
}

// Inherit dups ln's descriptor with close-on-exec cleared, so the descriptor
// survives syscall.Exec and the next image can adopt the listener instead of
// rebinding it.
func Inherit(ln net.Listener) (*os.File, error) {
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		return nil, fmt.Errorf("owner: inherit: %T is not a unix listener", ln)
	}
	f, err := ul.File()
	if err != nil {
		return nil, fmt.Errorf("owner: inherit: %w", err)
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_SETFD, 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("owner: clear close-on-exec: %w", err)
	}
	return f, nil
}
