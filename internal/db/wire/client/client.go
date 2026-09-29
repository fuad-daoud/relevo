//go:build unix

// Package client is the database/sql driver that speaks the owner protocol: it
// dials the socket, performs the handshake, and turns each driver connection
// into one pinned owner connection.
package client

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// DriverName is the name the wire driver registers under.
const DriverName = "relevo-owner"

// handshakeTimeout bounds dial plus handshake: a caller that must answer in two
// seconds cannot wait longer than one.
const handshakeTimeout = 2 * time.Second

func init() { sql.Register(DriverName, &Driver{}) }

// info is what welcome carries: the served database's schema version, the
// owner's embedded maximum, and the installation id a scoped handle needs.
// The value is unexported because the exported handshake is the Info function,
// which cannot share its name.
type info struct {
	Have   int
	Know   int
	Origin string
}

// Driver opens one wire connection per database/sql pooled connection.
type Driver struct{}

func (d *Driver) Open(name string) (driver.Conn, error) { return openConn(name) }

// Info dials sock, performs the handshake and returns the owner's answer. It
// closes its connection; the caller opens the handle it keeps separately.
func Info(ctx context.Context, sock string) (info, error) {
	nc, err := dialSock(ctx, sock)
	if err != nil {
		return info{}, err
	}
	defer func() { _ = nc.Close() }()
	c := &conn{nc: nc, w: wire.NewConn(nc)}
	if err := c.handshake(ctx); err != nil {
		return info{}, err
	}
	return info{Have: c.have, Know: c.know, Origin: c.origin}, nil
}

func dialSock(ctx context.Context, sock string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", sock)
}

func openConn(sock string) (driver.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	nc, err := dialSock(ctx, sock)
	if err != nil {
		return nil, err
	}
	c := &conn{nc: nc, w: wire.NewConn(nc)}
	if err := c.handshake(ctx); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return c, nil
}
