package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tursoCdcKVKey records a finished drop. It lives in the machine-local file for
// the reason the compress marker does: a shared copy would tell another
// installation that its own dead table was already gone.
const tursoCdcKVKey = "turso-cdc-drop.v1"

// cdcTableName is spelled in two pieces because the tree may not name the
// driver's private tables; a test greps every file for the whole word.
const cdcTableName = "turso" + "_cdc"

// freeBytes reads the space left under dir. It is a variable so a test can
// stand in a full disk without filling one.
var freeBytes = diskFree

// tursoCdcStats is what the marker holds: where the backups went and whether
// the reclaim steps succeeded.
type tursoCdcStats struct {
	DoneAt        time.Time `json:"done_at"`
	BackupPath    string    `json:"backup_path,omitempty"`
	CheckpointErr string    `json:"checkpoint_err,omitempty"`
	VacuumErr     string    `json:"vacuum_err,omitempty"`
}

// DropTursoCdcOnce removes the change-capture table an earlier engine left in
// the shared file and in the machine-local file beside it, once, after a full
// backup of each file that holds it. The table is dead weight: nothing reads
// it, and dropping it only frees pages once a VACUUM rewrites the file.
//
// A machine without the table writes the marker and takes no backup. A disk
// with less room in backupDir than the files being backed up is refused before
// anything is copied. backupPath names every backup taken, comma-separated.
func DropTursoCdcOnce(d *DB, backupDir string, now time.Time) (backupPath string, ran bool, err error) {
	marker := d.LocalOrSelf()
	if _, ok, kerr := marker.KVGet(tursoCdcKVKey); kerr != nil {
		return "", false, fmt.Errorf("turso cdc drop: kv get %s: %w", tursoCdcKVKey, kerr)
	} else if ok {
		return "", false, nil
	}

	holders, err := cdcHolders(d)
	if err != nil {
		return "", false, err
	}
	stats := tursoCdcStats{DoneAt: now}
	if len(holders) > 0 {
		if err := requireBackupSpace(holders, backupDir); err != nil {
			return "", false, err
		}
		stats, err = backupAndDrop(holders, backupDir, now, stats)
		if err != nil {
			return "", false, err
		}
	}
	value, err := json.Marshal(stats)
	if err != nil {
		return "", false, fmt.Errorf("turso cdc drop: marshal stats: %w", err)
	}
	if err := marker.KVPut(tursoCdcKVKey, value); err != nil {
		return "", false, fmt.Errorf("turso cdc drop: kv put %s: %w", tursoCdcKVKey, err)
	}
	return stats.BackupPath, true, nil
}

// cdcHolders returns the files of d's pair that still hold the table.
func cdcHolders(d *DB) ([]*DB, error) {
	files := []*DB{d}
	if l := d.Local(); l != nil && l != d {
		files = append(files, l)
	}
	var out []*DB
	for _, f := range files {
		var name string
		err := f.sqlDB.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, cdcTableName).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("turso cdc drop: look for the table in %s: %w", f.path, mapBusy(err))
		}
		out = append(out, f)
	}
	return out, nil
}

// requireBackupSpace refuses when backupDir cannot hold a full copy of every
// file about to be backed up and then the VACUUM after the drop, which rewrites
// the file through a copy of its own. Both land on the disk the live database
// is on, so a check for the backup alone would let the VACUUM fill it.
func requireBackupSpace(files []*DB, backupDir string) error {
	var need uint64
	for _, f := range files {
		info, err := os.Stat(f.path)
		if err != nil {
			return fmt.Errorf("turso cdc drop: size of %s: %w", f.path, err)
		}
		need += uint64(info.Size()) //nolint:gosec // a file size is never negative
	}
	free, err := freeBytes(backupDir)
	if err != nil {
		return fmt.Errorf("turso cdc drop: free space in %s: %w", backupDir, err)
	}
	need *= 2
	if free < need {
		return fmt.Errorf("turso cdc drop: refused: the backups and the vacuum need %d bytes and %s has %d free; free space there and restart the daemon: %w",
			need, backupDir, free, ErrInvalid)
	}
	return nil
}

// backupAndDrop backs each file up before dropping the table in it, then
// checkpoints and reclaims so the file shrinks. A checkpoint or reclaim failure
// is recorded rather than returned: the table is gone either way, and a reclaim
// the owner makes impossible is skipped rather than recorded.
func backupAndDrop(files []*DB, backupDir string, now time.Time, stats tursoCdcStats) (tursoCdcStats, error) {
	var paths []string
	for _, f := range files {
		path := filepath.Join(backupDir, filepath.Base(f.path)+".pre-cdc-drop-"+now.UTC().Format("20060102-150405"))
		if err := f.BackupTo(path); err != nil {
			return stats, err
		}
		paths = append(paths, path)
		if _, err := f.sqlDB.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+cdcTableName); err != nil {
			return stats, fmt.Errorf("turso cdc drop: drop in %s: %w", f.path, mapBusy(err))
		}
		fin := finishCompress(f, CompressStats{}, "change-capture table drop")
		stats.CheckpointErr = firstNonEmpty(stats.CheckpointErr, fin.CheckpointErr)
		stats.VacuumErr = firstNonEmpty(stats.VacuumErr, fin.VacuumErr)
	}
	stats.BackupPath = strings.Join(paths, ", ")
	return stats, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
