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
// one key of the shared config_doc, not a body of its own. The shared row stays
// the whole truth for each key and this layer says what this machine overrides,
// so the fields naming things here -- an account's config dir and home, the
// policy's serve settings -- never reach another machine.
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

// ConfigLocalPut upserts key's patch. It bumps config_meta.version like any
// other config write: the merged config the override changes is what a running
// server and the daemon must pick up on their next tick, and the version gate
// is the one counter both refreshers read. The bump is invisible to sync --
// the counter is never exported, and a local write records no revision and no
// write stamp -- so it advertises the change to this machine's readers only.
func (t *Tx) ConfigLocalPut(key string, patch []byte, now time.Time) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO config_local (key, patch, updated_at) VALUES (?, ?, ?)`,
		key, string(patch), formatTime(now)); err != nil {
		return fmt.Errorf("db: config local put %s: %w", key, mapBusy(err))
	}
	return t.bumpConfigVersion()
}

// ConfigLocalDelete removes key's override and bumps config_meta.version. An
// absent key writes nothing but still bumps: the version is a change counter
// for readers, not a count of stored overrides.
func (t *Tx) ConfigLocalDelete(key string) error {
	if _, err := t.exec(`DELETE FROM config_local WHERE key = ?`, key); err != nil {
		return fmt.Errorf("db: config local delete %s: %w", key, mapBusy(err))
	}
	return t.bumpConfigVersion()
}
