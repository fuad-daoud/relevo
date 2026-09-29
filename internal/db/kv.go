package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The schema-v2 key-value surface: one kv row holds one whole JSON document,
// exactly as the small state file it replaces held it. Reads come on both *DB
// and *Tx; writes are *Tx only, with *DB wrappers that open a transaction.

// KV is the minimal key-value surface the small stores share. Packages that
// must not import internal/store take a db.KV instead.
type KV interface {
	KVGet(key string) ([]byte, bool, error)
	KVPut(key string, value []byte) error
	KVDelete(key string) error
}

var _ KV = (*DB)(nil)

// KVGet returns the JSON document stored for key; ok is false when the row is
// absent. A schema-1 database reports every key as absent rather than erroring.
func (d *DB) KVGet(key string) ([]byte, bool, error) {
	return kvGet(context.Background(), d.sqlDB, key)
}

func (t *Tx) KVGet(key string) ([]byte, bool, error) { return kvGet(t.ctx, t.conn, key) }

func kvGet(ctx context.Context, q queryer, key string) ([]byte, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value_json FROM kv WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("db: kv get %s: %w", key, mapBusy(err))
	}
	return []byte(value), true, nil
}

// KVPut upserts key's whole document in one short transaction. value must be
// valid JSON, else ErrInvalid: the row's contract is the exact JSON document
// the file held.
func (d *DB) KVPut(key string, value []byte) error {
	return d.Tx(func(t *Tx) error { return t.KVPut(key, value) })
}

func (t *Tx) KVPut(key string, value []byte) error {
	if !json.Valid(value) {
		return fmt.Errorf("db: kv put %s: value is not valid JSON: %w", key, ErrInvalid)
	}
	if _, err := t.exec(`INSERT OR REPLACE INTO kv (key, value_json, updated_at) VALUES (?, ?, ?)`,
		key, string(value), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("db: kv put %s: %w", key, mapBusy(err))
	}
	return nil
}

func (d *DB) KVDelete(key string) error {
	return d.Tx(func(t *Tx) error { return t.KVDelete(key) })
}

func (t *Tx) KVDelete(key string) error {
	if _, err := t.exec(`DELETE FROM kv WHERE key = ?`, key); err != nil {
		return fmt.Errorf("db: kv delete %s: %w", key, mapBusy(err))
	}
	return nil
}

// KVKeys returns every key with the given prefix, sorted. A missing kv table
// (schema < 2) is an empty list.
func (d *DB) KVKeys(prefix string) ([]string, error) {
	return kvKeys(context.Background(), d.sqlDB, prefix)
}

func (t *Tx) KVKeys(prefix string) ([]string, error) { return kvKeys(t.ctx, t.conn, prefix) }

func kvKeys(ctx context.Context, q queryer, prefix string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM kv ORDER BY key`)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: kv keys: %w", mapBusy(err))
	}
	keys, err := collectRows(rows, scanString)
	if err != nil {
		return nil, fmt.Errorf("db: kv keys: %w", mapBusy(err))
	}
	var out []string
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out, nil
}

// PrefixKV wraps a KV so every key is namespaced with Prefix, which is how the
// serve-wide gate and availability records live under `serve.` in the one
// machine database without colliding with this machine's own rows.
type PrefixKV struct {
	KV     KV
	Prefix string
}

var _ KV = PrefixKV{}

func (p PrefixKV) KVGet(key string) ([]byte, bool, error) { return p.KV.KVGet(p.Prefix + key) }

func (p PrefixKV) KVPut(key string, value []byte) error { return p.KV.KVPut(p.Prefix+key, value) }

func (p PrefixKV) KVDelete(key string) error { return p.KV.KVDelete(p.Prefix + key) }

// KVKeys returns every key whose full name starts with Prefix + prefix, with
// Prefix stripped, so a caller sees the keys of its own namespace. The KV
// interface carries no KVKeys, so the store is asked through a narrow
// assertion; a store that cannot list keys yields none.
func (p PrefixKV) KVKeys(prefix string) ([]string, error) {
	lister, ok := p.KV.(interface {
		KVKeys(prefix string) ([]string, error)
	})
	if !ok {
		return nil, nil
	}
	keys, err := lister.KVKeys(p.Prefix + prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, strings.TrimPrefix(k, p.Prefix))
	}
	return out, nil
}

// KVTx is the kv surface inside one BEGIN IMMEDIATE transaction. *Tx and *DB
// both implement it.
type KVTx interface {
	KVGet(key string) ([]byte, bool, error)
	KVPut(key string, value []byte) error
	KVDelete(key string) error
	KVKeys(prefix string) ([]string, error)
}

// DBTxKV is the transactional kv surface the mastermind registry and the channel
// claims need: the four kv calls plus Tx to run a read-modify-write atomically.
type DBTxKV interface {
	KVTx
	Tx(fn func(KVTx) error) error
}

// TxKV adapts a *DB to DBTxKV: the four kv calls use the handle's own short
// transactions, and Tx narrows db's *Tx callback to KVTx.
type TxKV struct{ DB *DB }

var _ DBTxKV = TxKV{}

func (a TxKV) KVGet(key string) ([]byte, bool, error) { return a.DB.KVGet(key) }

func (a TxKV) KVPut(key string, value []byte) error { return a.DB.KVPut(key, value) }

func (a TxKV) KVDelete(key string) error { return a.DB.KVDelete(key) }

func (a TxKV) KVKeys(prefix string) ([]string, error) { return a.DB.KVKeys(prefix) }

func (a TxKV) Tx(fn func(KVTx) error) error {
	return a.DB.Tx(func(t *Tx) error { return fn(t) })
}
