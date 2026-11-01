//go:build unix

package main

import (
	"net"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// serveOwner starts the owner over d on ln, wiring onAbandoned as the hook
// the owner calls once when an unstoppable statement outlasts its grace. A
// nil handle means no socket and no server.
func serveOwner(d *db.DB, ln net.Listener, onAbandoned func()) (*owner.Server, error) {
	if d == nil || ln == nil {
		return nil, nil
	}
	srv := db.NewOwner(d)
	srv.OnAbandoned = onAbandoned
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
