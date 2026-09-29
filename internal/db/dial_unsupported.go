//go:build !unix

package db

import (
	"errors"
	"runtime"

	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

func notHere() error {
	return errors.New("relevo database dialling is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}

// Dial refuses off unix: the owner protocol is a unix socket.
func Dial(string) (*DB, error) { return nil, notHere() }

func dial(string, Options, bool) (*DB, error) { return nil, notHere() }

// NewOwner refuses off unix.
func NewOwner(*DB) *owner.Server { return owner.New(nil, 0, 0, "") }
