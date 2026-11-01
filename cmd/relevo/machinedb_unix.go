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

func ownerRouteSupported() bool { return true }

func ownerSocket(root string) (string, error) { return owner.SocketPath(root) }

func dialUDS(ctx context.Context, sock string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", sock)
}

// firstDial is dialWithStart's opening dial, a var so a test can hand it a
// refusal no real socket would answer.
var firstDial = dialUDS

// ownerStarting reports whether a starting daemon is evident: the connect
// comes first because probing the lock takes the flock, which would cost a
// daemon starting at that instant its start.
func ownerStarting(root, sock string) bool {
	if listenerHeld(sock) {
		return true
	}
	return daemonLockHeld(root)
}

// listenerHeld is one plain connect with no auto-start and no re-dial: a
// kernel-queued connection is the evidence a listener exists.
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
// once per process and reusing the answer. Like the daemon, it creates the
// state root and lock file.
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

// dialWithStart is the client dial hook: a sandboxed refusal returns at once,
// since waiting or starting cannot change it; otherwise it starts the owner
// and re-dials until the budget is spent. It never falls back to the file.
func dialWithStart(ctx context.Context, sock string) (net.Conn, error) {
	deadline := time.Now().Add(dbRouteBudget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	nc, firstErr := firstDial(ctx, sock)
	if firstErr == nil {
		return nc, nil
	}
	if dialSandboxDenied(firstErr) {
		return nil, fmt.Errorf("%w: %s: %w", errOwnerSandboxDenied, sock, firstErr)
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
