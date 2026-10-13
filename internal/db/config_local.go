package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The machine-local layer of the config doc. A row here is a merge-patch over
// one key of the shared config_doc rather than a body of its own: the shared
// row stays the whole truth for that key and this one says what this machine
// overrides, so the fields naming things here -- an account's config dir and
// home, the policy's serve settings -- never reach another machine and the
// shared value they belong to is still there to be read.
//
// The rows live in the machine-local file, so every caller reaches them through
// a handle bound by LocalOrSelf: a store built over a shared handle must not be
// able to read or write this machine's overrides out of the shared file.

// ConfigLocalGet returns the patch stored for key; ok is false when the key has
// no local override.
func (d *DB) ConfigLocalGet(key string) ([]byte, bool, error) {
	return configLocalGet(context.Background(), d.sqlDB, key)
}

func (t *Tx) ConfigLocalGet(key string) ([]byte, bool, error) {
	return configLocalGet(t.ctx, t.conn, key)
}

func configLocalGet(ctx context.Context, q queryer, key string) ([]byte, bool, error) {
	var patch string
	err := q.QueryRowContext(ctx, `SELECT patch FROM config_local WHERE key = ?`, key).Scan(&patch)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("db: config local get %s: %w", key, mapBusy(err))
	}
	return []byte(patch), true, nil
}

// ConfigLocalKeys returns every local override key starting with prefix, in
// sorted order.
func (d *DB) ConfigLocalKeys(prefix string) ([]string, error) {
	return configLocalKeys(context.Background(), d.sqlDB, prefix)
}

func (t *Tx) ConfigLocalKeys(prefix string) ([]string, error) {
	return configLocalKeys(t.ctx, t.conn, prefix)
}

func configLocalKeys(ctx context.Context, q queryer, prefix string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM config_local ORDER BY key`)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: config local keys %s: %w", prefix, mapBusy(err))
	}
	keys, err := collectRows(rows, scanString)
	if err != nil {
		return nil, fmt.Errorf("db: config local keys %s: %w", prefix, err)
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out, nil
}

// ConfigLocalPut upserts key's patch. It does not bump config_meta.version:
// an override that changes no shared section is not a shared-config change,
// and a reader's reload is driven by the sections themselves.
func (t *Tx) ConfigLocalPut(key string, patch []byte, now time.Time) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO config_local (key, patch, updated_at) VALUES (?, ?, ?)`,
		key, string(patch), formatTime(now)); err != nil {
		return fmt.Errorf("db: config local put %s: %w", key, mapBusy(err))
	}
	return nil
}

// ConfigLocalDelete removes key's override. An absent key writes nothing.
func (t *Tx) ConfigLocalDelete(key string) error {
	if _, err := t.exec(`DELETE FROM config_local WHERE key = ?`, key); err != nil {
		return fmt.Errorf("db: config local delete %s: %w", key, mapBusy(err))
	}
	return nil
}
