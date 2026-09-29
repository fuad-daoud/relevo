//go:build unix

package main

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
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
