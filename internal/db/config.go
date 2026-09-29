package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// The schema-v2 config and secrets surface. Reads come on both *DB and *Tx;
// writes are *Tx only, with *DB wrappers that open their own transaction. A
// database whose schema predates config_doc (schema < 2) reports every config
// and secret read as absent rather than erroring, so a read-only open
// (`relevo daemon --check`) sees an empty config, not a failure.

// hasConfig reports whether this database carries the config tables; only
// OpenReadOnly on a schema-1 file lacks them.
func (d *DB) hasConfig() bool { return d.have >= 2 }

// ConfigGet returns the JSON body stored for name; ok is false when the
// section is absent or the schema predates config_doc.
func (d *DB) ConfigGet(name string) ([]byte, bool, error) {
	if !d.hasConfig() {
		return nil, false, nil
	}
	return configGet(context.Background(), d.sqlDB, name)
}

func (t *Tx) ConfigGet(name string) ([]byte, bool, error) {
	return configGet(t.ctx, t.conn, name)
}

func configGet(ctx context.Context, q queryer, name string) ([]byte, bool, error) {
	var body string
	err := q.QueryRowContext(ctx, `SELECT body FROM config_doc WHERE name = ?`, name).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("db: config get %s: %w", name, mapBusy(err))
	}
	return []byte(body), true, nil
}

// ConfigPut upserts name's body and bumps config_meta.version by one; the
// caller owns the transaction, so a multi-section write is atomic.
func (t *Tx) ConfigPut(name string, body []byte, now time.Time) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO config_doc (name, body, updated_at) VALUES (?, ?, ?)`,
		name, string(body), formatTime(now)); err != nil {
		return fmt.Errorf("db: config put %s: %w", name, mapBusy(err))
	}
	return t.bumpConfigVersion()
}

// ConfigDelete removes name's body and bumps config_meta.version by one. A
// delete of an absent name still bumps: the version is a change counter for
// readers, not a count of stored sections.
func (t *Tx) ConfigDelete(name string) error {
	if _, err := t.exec(`DELETE FROM config_doc WHERE name = ?`, name); err != nil {
		return fmt.Errorf("db: config delete %s: %w", name, mapBusy(err))
	}
	return t.bumpConfigVersion()
}

func (t *Tx) bumpConfigVersion() error {
	if _, err := t.exec(`UPDATE config_meta SET version = version + 1 WHERE id = 1`); err != nil {
		return fmt.Errorf("db: config version: %w", mapBusy(err))
	}
	return nil
}

func (d *DB) ConfigVersion() (int64, error) {
	if !d.hasConfig() {
		return 0, nil
	}
	return configVersion(context.Background(), d.sqlDB)
}

func (t *Tx) ConfigVersion() (int64, error) {
	return configVersion(t.ctx, t.conn)
}

func configVersion(ctx context.Context, q queryer) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT version FROM config_meta WHERE id = 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("db: config version: %w", mapBusy(err))
	}
	return v, nil
}

// SecretGet returns name's value; ok is false when it is absent or the schema
// predates the secret table.
func (d *DB) SecretGet(name string) ([]byte, bool, error) {
	if !d.hasConfig() {
		return nil, false, nil
	}
	return secretGet(context.Background(), d.sqlDB, name)
}

func (t *Tx) SecretGet(name string) ([]byte, bool, error) {
	return secretGet(t.ctx, t.conn, name)
}

func secretGet(ctx context.Context, q queryer, name string) ([]byte, bool, error) {
	var value []byte
	err := q.QueryRowContext(ctx, `SELECT value FROM secret WHERE name = ?`, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("db: secret get %s: %w", name, mapBusy(err))
	}
	return value, true, nil
}

// SecretPut upserts name's value. It does not bump config_meta.version: that
// counter tracks the config sections the daemon reloads, and a secret is read
// once at construction.
func (t *Tx) SecretPut(name string, value []byte, now time.Time) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		name, value, formatTime(now)); err != nil {
		return fmt.Errorf("db: secret put %s: %w", name, mapBusy(err))
	}
	return nil
}

func (t *Tx) SecretDelete(name string) error {
	if _, err := t.exec(`DELETE FROM secret WHERE name = ?`, name); err != nil {
		return fmt.Errorf("db: secret delete %s: %w", name, mapBusy(err))
	}
	return nil
}

func (d *DB) SecretNames() ([]string, error) {
	if !d.hasConfig() {
		return nil, nil
	}
	rows, err := d.sqlDB.QueryContext(context.Background(), `SELECT name FROM secret ORDER BY name`)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: secret names: %w", mapBusy(err))
	}
	names, err := collectRows(rows, scanString)
	if err != nil {
		return nil, fmt.Errorf("db: secret names: %w", mapBusy(err))
	}
	return names, nil
}

// SecretStore adapts a *DB to the secret surface a caller with no transaction
// handle needs: a put or delete opens its own short transaction.
type SecretStore struct{ DB *DB }

func (s SecretStore) SecretGet(name string) ([]byte, bool, error) { return s.DB.SecretGet(name) }

func (s SecretStore) SecretPut(name string, value []byte, now time.Time) error {
	return s.DB.Tx(func(t *Tx) error { return t.SecretPut(name, value, now) })
}

func (s SecretStore) SecretDelete(name string) error {
	return s.DB.Tx(func(t *Tx) error { return t.SecretDelete(name) })
}

func (s SecretStore) SecretNames() ([]string, error) { return s.DB.SecretNames() }

// ConfigImportRecord appends one imported file to the audit trail, so the
// import is reconstructable after the file itself is deleted.
func (t *Tx) ConfigImportRecord(name, sourcePath string, body []byte, now time.Time) error {
	if _, err := t.exec(`INSERT INTO config_import (name, source_path, body, imported_at) VALUES (?, ?, ?, ?)`,
		name, sourcePath, body, formatTime(now)); err != nil {
		return fmt.Errorf("db: config import record %s: %w", name, mapBusy(err))
	}
	return nil
}

// OpenReadOnly opens path without ever migrating or writing it, for readers
// that must not create or change the database; a missing file's error wraps
// os.ErrNotExist.
func OpenReadOnly(path string) (*DB, error) {
	return openReadOnly(path)
}

func openReadOnly(path string) (_ *DB, err error) {
	if _, serr := os.Stat(path); serr != nil {
		return nil, fmt.Errorf("db: open readonly %s: %w", path, serr)
	}

	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open readonly %s: %w: %w", path, ErrOpen, err)
	}
	defer func() {
		if err != nil {
			if cerr := sqlDB.Close(); cerr != nil {
				err = fmt.Errorf("%w, and close failed: %w", err, cerr)
			}
		}
	}()

	if err = ping(sqlDB); err != nil {
		return nil, fmt.Errorf("db: open readonly %s: ping: %w: %w", path, ErrOpen, err)
	}

	have, err := maxVersion(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("db: open readonly %s: version: %w: %w", path, ErrOpen, err)
	}
	know, err := maxEmbedded(migrationFiles)
	if err != nil {
		return nil, fmt.Errorf("db: open readonly %s: migrations: %w: %w", path, ErrOpen, err)
	}

	return &DB{sqlDB: sqlDB, newer: have > know, have: have, know: know}, nil
}

// isMissingTable reports whether err is sqlite's "no such table" for a schema
// that predates the table, where the caller wants an absent answer.
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
