//go:build unix

package main

import (
	"net"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// serveOwner starts the owner over d on ln, wiring onAbandoned as the hook
// the owner calls once when an unstoppable statement outlasts its grace and
// onShortReadWAL as the one it calls once when a statement meets a poisoned
// pool. A nil handle means no socket and no server.
func serveOwner(d *db.DB, ln net.Listener, onAbandoned, onShortReadWAL func()) (*owner.Server, error) {
	if d == nil || ln == nil {
		return nil, nil
	}
	srv := db.NewOwner(d)
	srv.OnAbandoned = onAbandoned
	srv.OnShortReadWAL = onShortReadWAL
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
