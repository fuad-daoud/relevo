//go:build unix

package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExeIdentityChangesOnRename pins that a rename over the path (as `make
// install` does) reads as a new identity, but a repeat stat does not.
func TestExeIdentityChangesOnRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relevo")
	if err := os.WriteFile(path, []byte("one"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	first, err := ExeIdentity(path)
	if err != nil {
		t.Fatalf("ExeIdentity: %v", err)
	}
	again, err := ExeIdentity(path)
	if err != nil {
		t.Fatalf("ExeIdentity (no-op): %v", err)
	}
	if again != first {
		t.Errorf("identity changed with no write: %+v -> %+v", first, again)
	}

	tmp := filepath.Join(dir, "relevo.new")
	if err := os.WriteFile(tmp, []byte("two"), 0o755); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename over: %v", err)
	}

	second, err := ExeIdentity(path)
	if err != nil {
		t.Fatalf("ExeIdentity (after rename): %v", err)
	}
	if second == first {
		t.Error("identity unchanged after a rename over the file")
	}
}
