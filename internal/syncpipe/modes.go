package syncpipe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// SecureSyncFiles narrows the replica, its driver files and staged bodies to
// owner-only. They hold the whole database and content in transit, and an earlier
// looser umask may have left them readable. It runs before any worker opens the
// replica; a missing file is skipped.
func SecureSyncFiles(sharedPath string) error {
	files, err := relevosync.ReplicaFiles(ReplicaPath(sharedPath))
	if err != nil {
		return fmt.Errorf("syncpipe: list the replica files: %w", err)
	}
	staged, err := os.ReadDir(BlobStagingDir(sharedPath))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("syncpipe: list the staged bodies: %w", err)
	}
	for _, entry := range staged {
		if !entry.IsDir() {
			files = append(files, filepath.Join(BlobStagingDir(sharedPath), entry.Name()))
		}
	}
	for _, path := range files {
		if err := ownerOnly(path); err != nil {
			return err
		}
	}
	return nil
}

func ownerOnly(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("syncpipe: stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("syncpipe: secure %s: %w", path, err)
	}
	return nil
}
