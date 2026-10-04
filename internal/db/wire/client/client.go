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
// which cannot share its name. HasLocal says the owner serves a machine-local
// file beside the shared one, which is what tells a dial it may attach one.
type info struct {
	Have     int
	Know     int
	Origin   string
	PID      int
	Conns    int
	HasLocal bool
}

// Driver opens one wire connection per database/sql pooled connection.
type Driver struct{}

func (d *Driver) Open(name string) (driver.Conn, error) {
	return openConn(name, false, wire.ScopeShared)
}

// Connector dials sock and marks every handshake it opens ad-hoc, which a plain
// sql.Open(DriverName, sock) never does. A pool built from it with sql.OpenDB
// reaches the owner on the ad-hoc read path, which the owner may refuse while it
// reaps an abandoned statement.
func Connector(sock string, adHoc bool) driver.Connector {
	return ScopedConnector(sock, adHoc, wire.ScopeShared)
}

// ScopedConnector is Connector for one of the owner's two files: the shared
// database, or the machine-local file beside it. The scope goes in the
// handshake, so every pooled connection this connector opens is answered from
// that one file and a caller can never mix the two within a pool.
func ScopedConnector(sock string, adHoc bool, scope string) driver.Connector {
	return &connector{sock: sock, adHoc: adHoc, scope: scope}
}

// connector is the driver.Connector sql.OpenDB pools from.
type connector struct {
	sock  string
	adHoc bool
	scope string
}

// Connect opens one pooled connection. The handshake budget bounds dial plus
// handshake, exactly as Driver.Open does; the caller's context does not, so a
// pool that opens a connection mid-request keeps the same two-second bound.
func (c *connector) Connect(context.Context) (driver.Conn, error) {
	return openConn(c.sock, c.adHoc, c.scope)
}

func (c *connector) Driver() driver.Driver { return &Driver{} }

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
	return info{Have: c.have, Know: c.know, Origin: c.origin, PID: c.pid, Conns: c.conns, HasLocal: c.hasLocal}, nil
}

func dialSock(ctx context.Context, sock string) (net.Conn, error) {
	if dialer != nil {
		return dialer(ctx, sock)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", sock)
}

func openConn(sock string, adHoc bool, scope string) (driver.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), handshakeBudget)
	defer cancel()
	nc, err := dialSock(ctx, sock)
	if err != nil {
		return nil, err
	}
	c := &conn{nc: nc, w: wire.NewConn(nc), adHoc: adHoc, scope: scope}
	if err := c.handshake(ctx); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return c, nil
}
