package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const splitBackupPrefix = "relevo.db.pre-split-"

// PruneSplitBackups deletes every pre-split backup in backupDir except the one
// the first pass recorded in its marker and newest. A converge pass copies the
// whole shared file to move a handful of strays, and one runs on every start
// that finds any, so keeping them all grows by the database's size per start.
// The first pass's copy is the one that holds the rows as they were before any
// split; a converge copy only matters until the next one replaces it.
func PruneSplitBackups(d *DB, backupDir, newest string) error {
	keep := map[string]bool{}
	if newest != "" {
		keep[filepath.Base(newest)] = true
	}
	if local := d.Local(); local != nil {
		raw, ok, err := local.KVGet(splitKVKey)
		if err != nil {
			return fmt.Errorf("split: kv get %s: %w", splitKVKey, err)
		}
		var marker SplitStats
		if ok && json.Unmarshal(raw, &marker) == nil && marker.BackupPath != "" {
			keep[filepath.Base(marker.BackupPath)] = true
		}
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return fmt.Errorf("split: read backup dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, splitBackupPrefix) {
			continue
		}
		// A backup's -wal and -shm siblings live and die with it.
		base := strings.TrimSuffix(strings.TrimSuffix(name, "-wal"), "-shm")
		if keep[base] {
			continue
		}
		if err := os.Remove(filepath.Join(backupDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("split: prune %s: %w", name, err)
		}
	}
	return nil
}
