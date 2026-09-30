//go:build !unix

package main

import (
	"errors"
	"net"
	"runtime"
)

// servePprof has no socket transport off unix, so the flag is refused with the
// reason rather than silently profiled nothing.
func servePprof(string) (net.Listener, error) {
	return nil, errors.New("relevo daemon: --pprof needs a unix socket; not supported on " + runtime.GOOS)
}
