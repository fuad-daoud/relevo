//go:build unix

package syncpipe

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// TestSecureSyncFilesTightensTheReplicaSetAndStagedBodies pins the repair on
// start: the replica, each driver file named after it and each staged body end
// at 0600, a file belonging to something else keeps its mode, and a machine
// with none of the files is not an error.
func TestSecureSyncFilesTightensTheReplicaSetAndStagedBodies(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "relevo.db")
	if err := SecureSyncFiles(shared); err != nil {
		t.Fatalf("a machine with no sync files: %v", err)
	}

	replica := ReplicaPath(shared)
	var secret []string
	for _, suffix := range []string{"", "-wal", "-wal-revert", "-sibling"} {
		secret = append(secret, replica+suffix)
	}
	staging, err := EnsureBlobStagingDir(shared)
	if err != nil {
		t.Fatalf("EnsureBlobStagingDir: %v", err)
	}
	secret = append(secret, filepath.Join(staging, "body-1"))
	other := filepath.Join(filepath.Dir(shared), "notes.txt")
	for _, path := range append(secret, other) {
		writeMode(t, path, 0o644)
	}

	if err := SecureSyncFiles(shared); err != nil {
		t.Fatalf("SecureSyncFiles: %v", err)
	}
	for _, path := range secret {
		if got := permOf(t, path); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(path), got)
		}
	}
	if got := permOf(t, other); got != 0o644 {
		t.Errorf("an unrelated file was changed to %o", got)
	}
	if got := permOf(t, staging); got != 0o700 {
		t.Errorf("staging dir mode = %o, want 700", got)
	}
}
