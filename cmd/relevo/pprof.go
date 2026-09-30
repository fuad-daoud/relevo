//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
)

// sunPathLimit is the shortest sun_path among the supported unixes (macOS and
// the BSDs, at 104 bytes): a longer path can only fail at bind with an
// unhelpful EINVAL.
const sunPathLimit = 104

// pprofSocketMode keeps the profiling endpoints reachable only by the daemon's
// own user: a profile shows the process's memory and command line.
const pprofSocketMode = 0o600

// servePprof serves net/http/pprof on a unix socket and returns the listener
// whose lifetime the caller owns. Only a caller that asked for it calls this,
// so the endpoints exist exactly while an operator watches.
//
// A socket file left behind by an image that exec'd away is removed before the
// bind; anything else at path refuses, because deleting an operator's file to
// make room for a socket is not this function's call. The mux is local rather
// than http.DefaultServeMux, so importing the handlers cannot open endpoints
// the binary never meant to serve.
func servePprof(path string) (net.Listener, error) {
	if len(path) >= sunPathLimit {
		return nil, fmt.Errorf("pprof socket %s: path is %d bytes, over the %d-byte unix socket limit", path, len(path), sunPathLimit)
	}
	if err := removeStalePprofSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("pprof socket %s: %w", path, err)
	}
	if err := os.Chmod(path, pprofSocketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("pprof socket %s: %w", path, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() { _ = http.Serve(ln, mux) }()
	return ln, nil
}

// removeStalePprofSocket removes a socket file at path and leaves every other
// kind of file alone: the path is the operator's, and a typo should refuse,
// not destroy.
func removeStalePprofSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pprof socket %s: %w", path, err)
	}
	if fi.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("pprof socket %s: exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("pprof socket %s: %w", path, err)
	}
	return nil
}
