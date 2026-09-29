//go:build unix

package owner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPathRefusesAnOverLongPath(t *testing.T) {
	root := filepath.Join("/tmp", strings.Repeat("x", sunPathLimit))

	path, err := SocketPath(root)
	if err == nil {
		t.Fatalf("SocketPath(%q) = %q, nil; want a refusal", root, path)
	}
	if !strings.Contains(err.Error(), "sun_path") {
		t.Errorf("error %q must name the sun_path limit", err)
	}
	if !strings.Contains(err.Error(), "relevo.sock") {
		t.Errorf("error %q must name the socket path", err)
	}
}

func TestListenReplacesAStaleSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	path := filepath.Join(root, socketName)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	ln, err := Listen(root)
	if err != nil {
		t.Fatalf("Listen over a stale file: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Errorf("%s is mode %v, want a socket", path, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 0600", path, perm)
	}
}

func TestAdoptKeepsTheBoundSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	ln, err := Listen(root)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	path := filepath.Join(root, socketName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	f, err := Inherit(ln)
	if err != nil {
		t.Fatalf("Inherit: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	adopted, err := Adopt(int(f.Fd()))
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	t.Cleanup(func() { _ = adopted.Close() })

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after Adopt: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Errorf("adopt changed the socket file: %v is not %v", after, before)
	}
}
