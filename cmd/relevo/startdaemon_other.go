//go:build !unix

package main

import "errors"

// startDaemon refuses off unix: the owner is served over a unix socket, so
// there is no local daemon to start.
func startDaemon() error {
	return errors.New("relevo daemon: auto-start is not supported on this platform")
}

func relevoUnitInstalled() bool { return false }

func daemonPlistInstalled() bool { return false }

// daemonStop refuses off unix: the owner is served over a unix socket, so there
// is no local daemon to stop.
func daemonStop() error {
	return errors.New("relevo daemon: stop is not supported on this platform")
}
