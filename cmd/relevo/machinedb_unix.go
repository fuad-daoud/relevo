//go:build unix

package main

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ownerRouteSupported reports that this platform can serve and dial the owner.
func ownerRouteSupported() bool { return true }

// ownerSocket resolves the socket the owner serves under root.
func ownerSocket(root string) (string, error) { return owner.SocketPath(root) }

// dialUDS is one plain unix dial.
func dialUDS(ctx context.Context, sock string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", sock)
}

// ownerStarting reports whether a daemon that is still starting is evident
// under root: a plain connect to sock that succeeds means some process holds
// the listener, which is what survives a re-exec; when the connect fails the
// daemon lock is probed. The connect comes first because DaemonRunning takes
// the flock for a moment, which would cost a daemon that takes it at that
// instant its start.
func ownerStarting(root, sock string) bool {
	if listenerHeld(sock) {
		return true
	}
	return daemonLockHeld(root)
}

// listenerHeld is one plain connect, with no auto-start and no re-dial: a
// connection the kernel queues in the backlog is the evidence that a listener
// exists, whether or not anyone is accepting yet.
func listenerHeld(sock string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), statuslineDialBudget)
	defer cancel()
	nc, err := dialUDS(ctx, sock)
	if err != nil {
		return false
	}
	_ = nc.Close()
	return true
}

// The lock probe is remembered: taking the flock even for a moment races a
// daemon that is starting, so each state root is probed at most once per
// process.
var (
	daemonLockProbeMu sync.Mutex
	daemonLockProbed  = map[string]bool{}
)

// daemonLockHeld reports whether a daemon holds the lock under root, probing
// the lock once per process and reusing the answer. It also creates the state
// root and the lock file, exactly as the daemon itself does.
func daemonLockHeld(root string) bool {
	daemonLockProbeMu.Lock()
	defer daemonLockProbeMu.Unlock()
	if held, ok := daemonLockProbed[root]; ok {
		return held
	}
	running, err := store.New(root).DaemonRunning()
	held := err == nil && running
	daemonLockProbed[root] = held
	return held
}

// dialWithStart is the client dial hook: it dials, starts the owner when the
// socket does not answer, then re-dials with jittered steps until the budget
// is spent. It never falls back to opening the file.
func dialWithStart(ctx context.Context, sock string) (net.Conn, error) {
	deadline := time.Now().Add(dbRouteBudget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if nc, err := dialUDS(ctx, sock); err == nil {
		return nc, nil
	}
	if err := daemonStarter(); err != nil {
		// A failed start is not fatal here: a concurrent starter may already
		// be coming up, and the re-dial loop below is the answer either way.
		_ = err
	}
	for {
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("%w: %s", errOwnerUnavailable, sock)
		}
		step := time.Duration(25+rand.Intn(76)) * time.Millisecond
		if remaining := time.Until(deadline); step > remaining {
			step = remaining
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %s: %w", errOwnerUnavailable, sock, ctx.Err())
		case <-time.After(step):
		}
		if nc, err := dialUDS(ctx, sock); err == nil {
			return nc, nil
		}
	}
}
