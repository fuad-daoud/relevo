//go:build unix

package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
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

// DialContextAdHoc is DialContext for the ad-hoc read path (`db query`): every
// connection its pool opens marks itself in the handshake, so the owner may
// refuse a request while it is reaping an abandoned statement. A refusal is an
// answer the caller must accept rather than fall back from: a direct open would
// find the file held and report a lock conflict that is not the real problem.
func DialContextAdHoc(ctx context.Context, sock string) (*DB, error) {
	return dialContext(ctx, sock, Options{AdHoc: true}, true)
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

	sqlDB := sql.OpenDB(client.Connector(sock, o.AdHoc))
	handle := &DB{
		sqlDB:      sqlDB,
		have:       info.Have,
		know:       know,
		newer:      info.Have > know,
		origin:     origin,
		beginRetry: retry,
		route:      "owner " + sock,
		sock:       sock,
	}
	// The owner serves the machine-local file beside the shared one, so the
	// dial path attaches it exactly as a direct open does. That is the whole of
	// the split as an owner-routed reader sees it: the handle names both files,
	// the sync rows and the token are reachable, and nothing about the routing
	// table changes -- handles move, rows stay where the classification put them.
	//
	// The attachment is the owner's own answer rather than a path computed here:
	// the socket already belongs to one state root, so the local file it names is
	// that root's companion and never another root's. A caller that wanted a
	// different root's file has no way to name it here.
	if info.HasLocal {
		handle.local = dialLocal(sock, o, info.Have, know, retry, origin)
	}
	return handle, nil
}

// dialLocal opens the machine-local file beside the shared one, over the same
// socket and under the same options, as a handle of its own. It carries no path:
// the owner knows which file its socket belongs to, and a name this process
// rebuilt could disagree with the one actually open -- which is the mistake that
// would point one root's reader at another's file.
//
// have is the shared file's version, which is the local file's too: both run the
// same migration series on every open, so one handshake answers for both and
// there is nothing to ask the owner twice.
func dialLocal(sock string, o Options, have, know int, retry time.Duration, origin string) *DB {
	return &DB{
		sqlDB:      sql.OpenDB(client.ScopedConnector(sock, o.AdHoc, wire.ScopeLocal)),
		have:       have,
		know:       know,
		newer:      have > know,
		origin:     origin,
		beginRetry: retry,
		route:      "owner " + sock + " local",
	}
}

// Sock is the owner socket this handle dialled, empty on a direct open. It is
// what a request that needs the owner itself rather than a statement -- a sync
// verb -- dials: the verb runs against the owner's own handles, so it needs
// the stream, not this handle's pool.
func (d *DB) Sock() string { return d.sock }

// SyncVerb runs one sync verb against the owner serving this handle, which is
// how a client asks the daemon to do the work: the owner runs it with the
// handles it already holds, so this process never opens the file and never
// takes the lock the daemon is holding.
//
// The token travels inside the framed request and is not formatted into any
// error, log line or result on the way; the only token field on the answer is a
// bool. A handle opened directly has no socket and refuses rather than opening
// one: the verb exists precisely so the writing verbs stop needing a direct
// open, and a fallback would put that open back.
//
// The error keeps the client's own classification: a verb whose frame reached
// the owner and whose reply the caller's deadline ended stays marked as such
// through this wrap, so a caller can tell a request in progress from one that
// never arrived.
func (d *DB) SyncVerb(ctx context.Context, verb *wire.SyncVerb, token []byte) (*wire.SyncResult, error) {
	if d.sock == "" {
		return nil, fmt.Errorf("db: sync verb: this handle did not dial an owner: %w", ErrOpen)
	}
	res, err := client.SyncVerb(ctx, d.sock, verb, token)
	if err != nil {
		return nil, fmt.Errorf("db: sync verb %s: %w: %w", verb.Verb, ErrOpen, err)
	}
	return res, nil
}

// SyncFreshen tells the owner serving this handle that a reader wants current
// data. It waits for no answer and is best effort: a handle that did not dial
// an owner has nobody to tell.
func (d *DB) SyncFreshen(ctx context.Context) error {
	if d.sock == "" {
		return fmt.Errorf("db: sync freshen: this handle did not dial an owner: %w", ErrOpen)
	}
	return client.SyncFreshen(ctx, d.sock)
}

// NewOwner serves d on a listener the caller opened, and the machine-local
// file beside it when d carries one. The owner and this package share nothing
// else: the server takes the raw pools and the handle's facts. Both handles are
// marked served so a vacuum refuses to swap either pool out from under the
// owner's clients.
func NewOwner(d *DB) *owner.Server {
	d.served = true
	srv := owner.New(d.sqlDB, d.have, d.know, d.origin, errCode)
	if l := d.local; l != nil {
		l.served = true
		srv.ServeLocal(l.sqlDB)
	}
	return srv
}
