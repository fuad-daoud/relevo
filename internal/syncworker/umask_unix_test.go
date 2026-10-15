//go:build unix

package syncworker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestOwnerOnlyFilesNarrowsAPermissiveUmask pins that a file created after the
// call is 0600 even when the process started with umask 0, which is how the
// driver's own files would otherwise come out world-readable.
func TestOwnerOnlyFilesNarrowsAPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	OwnerOnlyFiles()
	path := filepath.Join(t.TempDir(), "relevo-sync.db-wal")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = f.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}
