//go:build !unix

package main

import (
	"net"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// serveOwner starts the owner over d on ln. A nil handle (the open failed)
// means no socket and no server, which is every off-unix start: there is no
// socket transport there. The WAL hook is unset off unix with it.
func serveOwner(d *db.DB, ln net.Listener, _, _ func()) (*owner.Server, error) {
	if d == nil || ln == nil {
		return nil, nil
	}
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
