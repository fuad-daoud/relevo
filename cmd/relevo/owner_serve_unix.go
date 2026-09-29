//go:build unix

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// listenFDEnv names the inherited listener descriptor a re-exec passes to the
// image that replaces it.
const listenFDEnv = "RELEVO_LISTEN_FD"

// daemonDrainWindow bounds how long the daemon lets an open transaction finish
// before it drops the clients and execs the new image.
const daemonDrainWindow = 5 * time.Second

// handoffFD holds the inherited listener descriptor so its finalizer cannot
// close it before syscall.Exec runs. It is set once, on the re-exec path, and
// released by closeHandoff when the exec never happened.
var handoffFD *os.File

// checkSocketPath refuses a state root whose socket path cannot fit sun_path.
// It runs under the lock and before newRuntime, so a winning start refuses with
// one line before it mints or migrates anything.
func checkSocketPath(root string) error {
	_, err := owner.SocketPath(root)
	return err
}

// openOwnerListener binds the owner socket, or adopts the listener an earlier
// image passed in RELEVO_LISTEN_FD. Adoption never unlinks or rebinds, so
// connections queued across the exec are served by this image.
func openOwnerListener(root string) (net.Listener, error) {
	if v := os.Getenv(listenFDEnv); v != "" {
		fd, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("relevo: %s=%q: %w", listenFDEnv, v, err)
		}
		return owner.Adopt(fd)
	}
	return owner.Listen(root)
}

// serveOwner starts the owner over d on ln. A nil handle (the open failed)
// means no socket and no server.
func serveOwner(d *db.DB, ln net.Listener) (*owner.Server, error) {
	if d == nil || ln == nil {
		return nil, nil
	}
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

// drainAndHandoff drains the owner and returns the inherited listener's
// descriptor for the next image. Draining before the caller closes the database
// is what lets an open transaction commit; the dup has close-on-exec cleared so
// the descriptor survives syscall.Exec.
func drainAndHandoff(srv *owner.Server, ln net.Listener) (int, error) {
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), daemonDrainWindow)
		_ = srv.Drain(ctx)
		cancel()
	}
	if ln == nil {
		return -1, nil
	}
	f, err := owner.Inherit(ln)
	if err != nil {
		return -1, err
	}
	handoffFD = f
	return int(f.Fd()), nil
}

// closeHandoff releases the inherited descriptor when the exec never happened,
// so a failed re-exec leaves no leaked descriptor behind.
func closeHandoff() {
	if handoffFD != nil {
		_ = handoffFD.Close()
		handoffFD = nil
	}
}
