//go:build unix

package owner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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
	// The socket must survive the close below, the way the daemon's survives a
	// re-exec: unlink-on-close is off.
	if ul, ok := ln.(interface{ SetUnlinkOnClose(bool) }); ok {
		ul.SetUnlinkOnClose(false)
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

	// Adopt takes ownership of the descriptor it is given, so the test hands it
	// its own dup: the inherited file must stay open for the comparison below.
	dup, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	adopted, err := Adopt(dup)
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

	// Closing the original listener does not unlink the socket the adopted one
	// still serves: the same socket file is there afterwards.
	if err := ln.Close(); err != nil {
		t.Fatalf("close the original listener: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the socket file is gone after closing the original listener: %v", err)
	}
}

// TestAdoptClosesTheDescriptorItWasGiven pins the ownership: Adopt dups the
// listener's descriptor with net.FileListener and closes the one it was handed,
// so the caller's fd number is not left held by an *os.File whose finalizer
// would close it again later.
func TestAdoptClosesTheDescriptorItWasGiven(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	ln, err := Listen(root)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	f, err := Inherit(ln)
	if err != nil {
		t.Fatalf("Inherit: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	dup, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	adopted, err := Adopt(dup)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	t.Cleanup(func() { _ = adopted.Close() })

	if _, err := unix.FcntlInt(uintptr(dup), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Errorf("F_GETFD on the handed fd = %v, want EBADF: Adopt left it open", err)
	}
}
