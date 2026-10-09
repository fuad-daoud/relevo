//go:build !unix

package db

import (
	"context"
	"errors"
	"runtime"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

func notHere() error {
	return errors.New("relevo database dialling is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}

// Dial refuses off unix: the owner protocol is a unix socket.
func Dial(string) (*DB, error) { return nil, notHere() }

// DialContext refuses off unix: the owner protocol is a unix socket.
func DialContext(context.Context, string) (*DB, error) { return nil, notHere() }

// DialContextAdHoc refuses off unix like DialContext: the owner protocol is a
// unix socket, so the ad-hoc read path has no owner to mark itself to.
func DialContextAdHoc(context.Context, string) (*DB, error) { return nil, notHere() }

func dial(string, Options, bool) (*DB, error) { return nil, notHere() }

// Sock is always empty off unix: no handle ever dialled an owner here.
func (d *DB) Sock() string { return d.sock }

// SyncVerb refuses off unix: the verb rides the owner socket, which is a unix
// socket. Every writer verb therefore refuses off unix rather than falling back
// to a direct open -- the fallback is the file-lock conflict this route exists
// to remove, and it would put it straight back.
func (d *DB) SyncVerb(context.Context, *wire.SyncVerb, []byte) (*wire.SyncResult, error) {
	return nil, notHere()
}

// SyncFreshen refuses off unix for the same reason SyncVerb does.
func (d *DB) SyncFreshen(context.Context) error { return notHere() }

// NewOwner refuses off unix.
func NewOwner(*DB) *owner.Server { return owner.New(nil, 0, 0, "", nil) }
