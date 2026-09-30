//go:build unix

package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// dialTimeout bounds dial plus handshake: a surface that must answer in two
// seconds cannot wait longer than one.
const dialTimeout = 2 * time.Second

// Dial connects to an owner over sock and returns the same *DB a direct Open
// would: the schema answer comes from the handshake, the origin from the
// owner, and a nil error means the handshake completed. Dial plus the handshake
// share dialTimeout.
func Dial(sock string) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	return DialContext(ctx, sock)
}

// DialContext is Dial under the caller's ctx: the deadline ctx carries bounds
// dial and handshake together, so a caller that may wait longer than
// dialTimeout -- a verb waiting for a daemon that is still starting -- sets its
// own limit here instead of being cut off by the two-second one.
func DialContext(ctx context.Context, sock string) (*DB, error) {
	return dialContext(ctx, sock, Options{}, true)
}

// dial builds the handle under its own dialTimeout deadline. adoptOrigin
// selects between the owner's installation id (production) and the caller's
// own Options (the test hop, which must carry the caller's origin verbatim).
func dial(sock string, o Options, adoptOrigin bool) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	return dialContext(ctx, sock, o, adoptOrigin)
}

// dialContext builds the handle under ctx.
func dialContext(ctx context.Context, sock string, o Options, adoptOrigin bool) (*DB, error) {
	info, err := client.Info(ctx, sock)
	if err != nil {
		return nil, fmt.Errorf("db: dial %s: %w: %w", sock, ErrOpen, err)
	}
	// know is this binary's embedded maximum, exactly what open computes; only
	// have is the owner's answer, so a client older than the database reports
	// Newer() and its callers decide what to do.
	know, err := maxEmbedded(migrationFiles)
	if err != nil {
		return nil, fmt.Errorf("db: dial %s: migrations: %w: %w", sock, ErrOpen, err)
	}
	retry := beginRetryFor
	if o.BeginRetry > 0 {
		retry = o.BeginRetry
	}
	origin := o.Origin
	if adoptOrigin {
		origin = info.Origin
	}

	sqlDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		return nil, fmt.Errorf("db: dial %s: %w: %w", sock, ErrOpen, err)
	}
	return &DB{
		sqlDB:      sqlDB,
		have:       info.Have,
		know:       know,
		newer:      info.Have > know,
		origin:     origin,
		beginRetry: retry,
		route:      "owner " + sock,
	}, nil
}

// NewOwner serves d on a listener the caller opened. The owner and this
// package share nothing else: the server takes the raw pool and the handle's
// facts.
func NewOwner(d *DB) *owner.Server {
	return owner.New(d.sqlDB, d.have, d.know, d.origin)
}
