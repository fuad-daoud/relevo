//go:build unix

package main

import (
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

// pprofSocketPath returns a socket path short enough for sun_path on every
// supported unix: a t.TempDir under macOS's /var/folders is not.
func pprofSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := dir + "/pprof.sock"
	if len(path) >= sunPathLimit {
		t.Fatalf("socket path %q is %d bytes, over the %d-byte sun_path", path, len(path), sunPathLimit)
	}
	return path
}

// pprofGet fetches path over the unix socket at sock and returns the status and
// the whole body.
func pprofGet(t *testing.T, sock, path string) (int, string) {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	resp, err := client.Get("http://pprof" + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

// TestDaemonFlagPprof pins that the flag carries the socket path the daemon
// binds, and is empty -- the off switch -- when nobody asked.
func TestDaemonFlagPprof(t *testing.T) {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	v := daemonFlagSet(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if *v.pprof != "" {
		t.Errorf("default pprof = %q, want empty", *v.pprof)
	}

	fs = flag.NewFlagSet("daemon", flag.ContinueOnError)
	v = daemonFlagSet(fs)
	if err := fs.Parse([]string{"--pprof", "/run/user/1000/relevo-pprof.sock"}); err != nil {
		t.Fatalf("Parse(--pprof): %v", err)
	}
	if *v.pprof != "/run/user/1000/relevo-pprof.sock" {
		t.Errorf("pprof = %q, want the parsed path", *v.pprof)
	}
}

// TestServePprofGoroutineDump pins that the socket serves the goroutine dump a
// SIGQUIT cannot: the daemon must stay alive to be profiled.
func TestServePprofGoroutineDump(t *testing.T) {
	sock := pprofSocketPath(t)
	ln, err := servePprof(sock)
	if err != nil {
		t.Fatalf("servePprof: %v", err)
	}
	defer func() { _ = ln.Close() }()

	code, body := pprofGet(t, sock, "/debug/pprof/goroutine?debug=2")
	if code != http.StatusOK {
		t.Fatalf("goroutine status = %d, want 200", code)
	}
	if !strings.Contains(body, "goroutine") {
		t.Errorf("goroutine body lacks a stack: %.200q", body)
	}
}

// TestServePprofSocketIsOwnerOnly pins the mode: the endpoints show the
// process's memory and command line, so any other account reaching the socket
// is a leak.
func TestServePprofSocketIsOwnerOnly(t *testing.T) {
	sock := pprofSocketPath(t)
	ln, err := servePprof(sock)
	if err != nil {
		t.Fatalf("servePprof: %v", err)
	}
	defer func() { _ = ln.Close() }()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != pprofSocketMode {
		t.Errorf("socket mode = %o, want %o", got, pprofSocketMode)
	}
}

// TestServePprofReplacesStaleSocket pins the re-exec path: an image replaced by
// syscall.Exec runs no defers, so the next image must remove the socket file it
// finds rather than refuse to start its own.
func TestServePprofReplacesStaleSocket(t *testing.T) {
	sock := pprofSocketPath(t)
	old, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("seed socket: %v", err)
	}
	if ul, ok := old.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close seed socket: %v", err)
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("stale socket not left behind: %v", err)
	}

	ln, err := servePprof(sock)
	if err != nil {
		t.Fatalf("servePprof over a stale socket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	code, _ := pprofGet(t, sock, "/debug/pprof/")
	if code != http.StatusOK {
		t.Errorf("status over the rebound socket = %d, want 200", code)
	}
}

// TestServePprofRefusesAnOverlongPath pins the platform limit: a path over
// sun_path must refuse with the reason, not reach bind's EINVAL. macOS's
// temp directories are long enough to hit it.
func TestServePprofRefusesAnOverlongPath(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", sunPathLimit) + "/pprof.sock"
	if _, err := servePprof(long); err == nil {
		t.Fatal("servePprof accepted a path over the sun_path limit")
	} else if !strings.Contains(err.Error(), "unix socket limit") {
		t.Errorf("err = %v, want it to name the socket limit", err)
	}
}

// TestServePprofRefusesNonSocket pins that a path holding anything but a stale
// socket refuses loudly instead of deleting the operator's file.
func TestServePprofRefusesNonSocket(t *testing.T) {
	sock := pprofSocketPath(t)
	if err := os.WriteFile(sock, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if _, err := servePprof(sock); err == nil {
		t.Fatal("servePprof accepted a non-socket path")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("the file was removed: %v", err)
	}
}

// TestServePprofStopsWithItsListener pins that closing the listener ends the
// serving goroutine rather than leaving the socket answering.
func TestServePprofStopsWithItsListener(t *testing.T) {
	sock := pprofSocketPath(t)
	ln, err := servePprof(sock)
	if err != nil {
		t.Fatalf("servePprof: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	if resp, err := client.Get("http://pprof/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Error("socket still answers after Close")
	}
}
