//go:build !unix

package main

import (
	"net"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// serveOwner serves nothing off unix: a nil handle means no socket and no
// server, and there is no socket transport there.
func serveOwner(d *db.DB, ln net.Listener, _ func()) (*owner.Server, error) {
	if d == nil || ln == nil {
		return nil, nil
	}
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
