//go:build !unix

package main

import (
	"errors"
	"net"
	"runtime"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// listenFDEnv names the inherited listener descriptor; off unix nothing is ever
// inherited.
const listenFDEnv = "RELEVO_LISTEN_FD"

func unsupportedOwner() error {
	return errors.New("relevo database ownership is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}

func checkSocketPath(string) error { return unsupportedOwner() }

func openOwnerListener(string) (net.Listener, error) { return nil, unsupportedOwner() }

func serveOwner(*db.DB, net.Listener) (*owner.Server, error) { return nil, unsupportedOwner() }

func drainAndHandoff(*owner.Server, net.Listener) (int, error) { return -1, unsupportedOwner() }

func closeHandoff() {}
