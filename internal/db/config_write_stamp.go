package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ConfigWriteStamp is the stamp recorded when key was last written on this
// machine; ok is false when no write here has stamped it. It binds the
// machine-local file, where the table lives.
func (d *DB) ConfigWriteStamp(key string) (stamp string, ok bool, err error) {
	err = d.LocalOrSelf().sqlDB.QueryRowContext(context.Background(),
		`SELECT stamp FROM config_write_stamp WHERE key = ?`, key).Scan(&stamp)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: config write stamp %s: %w", key, mapBusy(err))
	}
	return stamp, true, nil
}

// ConfigWriteStampPut records the stamp of a write of key.
func (t *Tx) ConfigWriteStampPut(key, stamp string) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO config_write_stamp (key, stamp) VALUES (?, ?)`,
		key, stamp); err != nil {
		return fmt.Errorf("db: config write stamp %s: %w", key, mapBusy(err))
	}
	return nil
}

// ConfigWriteStamp is DB.ConfigWriteStamp read inside the caller's transaction.
func (t *Tx) ConfigWriteStamp(key string) (stamp string, ok bool, err error) {
	err = t.queryRow(`SELECT stamp FROM config_write_stamp WHERE key = ?`, key).Scan(&stamp)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: config write stamp %s: %w", key, mapBusy(err))
	}
	return stamp, true, nil
}
