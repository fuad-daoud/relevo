package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The two-file split. Every table and every kv namespace has exactly one home:
// the shared database, or the machine-local file beside it. The classification
// is written once, here, and one pass moves what the shared file is holding.
// Both files run the same migrations, so placement is a routing decision and
// never a schema fork.

// splitKVKey is the local kv key a finished split pass is recorded under. It
// is machine-local itself: a shared copy of the marker would tell another
// installation's pass that the move is done here too.
const splitKVKey = "split-local.v1"

// splitLocalTables is every table whose rows mean something only on the
// machine that wrote them, so they never leave it: pids, absolute paths,
// offsets, listen addresses, secrets, and this machine's own configuration.
var splitLocalTables = []string{
	"secret",
	"session_consent",
	"ingest_cursor",
	"config_import",
	"config_doc",
	"config_revision",
	"config_meta",
}

// splitLocalNamespaces is the kv allowlist, one entry per namespace or key. A
// key belongs to the local file when it equals an entry or extends it on a
// namespace boundary, so `serve.clients` is local under `serve` while
// `servers` is not local under `serve`.
var splitLocalNamespaces = []string{
	"daemon",
	"serve",
	"planner",
	"mastermind",
	"claim",
	"hooks.log",
	"ledger",
	"availability",
	"latency",
	"ui",
	"agents-manifest",
	"release-check",
	"ingested.archive",
	"mirror-dedupe",
	"zstd-compress.v1",
	"origin-backfill.v1",
	"split-local.v1",
	"sync",
}

// SplitPath is the machine-local file beside a shared database: the shared
// name with `-local` before its extension, in the same directory. A path
// without an extension gains the suffix whole, so every open has exactly one
// local companion and two opens can never collide on it.
func SplitPath(shared string) string {
	ext := filepath.Ext(shared)
	if ext == "" {
		return shared + "-local"
	}
	return strings.TrimSuffix(shared, ext) + "-local" + ext
}

// OpenSplit opens the shared database at path and the machine-local file
// beside it, applying the same migrations to both, and returns the shared
// handle with the local one attached. Both files run the whole migration
// series: the split is a placement decision, so the schema is never forked
// and every existing call site keeps working against both.
func OpenSplit(path string, o Options) (*DB, error) {
	shared, err := open(path, o)
	if err != nil {
		return nil, err
	}
	local, err := open(SplitPath(path), o)
	if err != nil {
		if cerr := shared.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}
	shared.local = local
	return shared, nil
}

// Local is the machine-local file beside this handle, or nil on a handle
// opened without one. A pass that moves rows between the two files reads it;
// nothing else needs it.
func (d *DB) Local() *DB { return d.local }

// splitFile names which of the two files a scope's rows live in.
type splitFile int

const (
	splitShared splitFile = iota
	splitLocal
)

// splitTableFile names the file a table's rows live in. A table neither the
// local allowlist nor the shared history names is shared: an unclassified
// table is history, and history is the safe default for an unknown scope.
func splitTableFile(table string) splitFile {
	for _, name := range splitLocalTables {
		if name == table {
			return splitLocal
		}
	}
	return splitShared
}

// splitKeyFile names the file a kv key's row lives in. The match is the exact
// key or an extension on a namespace boundary, never a bare prefix: a shared
// key must not be captured by a local namespace that merely starts the same
// way.
func splitKeyFile(key string) splitFile {
	for _, ns := range splitLocalNamespaces {
		if key == ns || strings.HasPrefix(key, ns+".") || strings.HasPrefix(key, ns+"/") {
			return splitLocal
		}
	}
	return splitShared
}

// SplitStats records one finished pass, both in the local kv row and in the
// daemon's log.
type SplitStats struct {
	DoneAt      time.Time `json:"done_at"`
	BackupPath  string    `json:"backup_path,omitempty"`
	RowsMoved   int64     `json:"rows_moved"`
	KVKeysMoved int64     `json:"kv_keys_moved"`
}

// splitTable names one local table, the columns its rows carry, and the
// natural key the move deletes on.
type splitTable struct {
	name    string
	columns []string
	key     []int
	// singleton marks a table whose schema seeds a row, so its presence is
	// not evidence of a row worth moving.
	singleton bool
}

func splitTables() []splitTable {
	return []splitTable{
		{
			name:    "secret",
			columns: []string{"name", "value", "updated_at"},
			key:     []int{0},
		},
		{
			name:    "session_consent",
			columns: []string{"harness_kind", "session_id", "answer", "answer_at", "told", "told_at"},
			key:     []int{0, 1},
		},
		{
			name:    "ingest_cursor",
			columns: []string{"source", "byte_offset", "head_sha", "whole_sha", "updated_at"},
			key:     []int{0},
		},
		{
			name:    "config_import",
			columns: []string{"id", "name", "source_path", "body", "imported_at"},
			key:     []int{0},
		},
		{
			name:    "config_doc",
			columns: []string{"name", "body", "updated_at"},
			key:     []int{0},
		},
		{
			name:    "config_revision",
			columns: []string{"rev", "at", "source", "message", "version", "changes", "snapshot"},
			key:     []int{0},
		},
		{
			name:      "config_meta",
			columns:   []string{"id", "version"},
			key:       []int{0},
			singleton: true,
		},
	}
}

// splitBeforeTable runs before each table's rows move. It is a var so a test
// can fail a pass between two tables and pin what a half-finished pass leaves.
var splitBeforeTable = func(table string) error { return nil }

// SplitOnce moves every machine-local row out of the shared database into the
// local file beside it, once per pair and only after a full backup. An
// existing local kv row means an earlier pass finished, so ran is false. An
// empty candidate set writes the marker and returns with no backup. Otherwise
// the shared file is backed up first -- a failure returns the error with
// nothing moved and no marker -- then each table's rows are copied and
// deleted, and the marker is written last, so a failure anywhere before it
// leaves the next start retrying from the top.
func SplitOnce(d *DB, backupDir string, now time.Time) (stats SplitStats, ran bool, err error) {
	local := d.Local()
	if local == nil {
		return SplitStats{}, false, fmt.Errorf("split: %s was opened without a local file: %w", d.path, ErrInvalid)
	}
	if _, ok, kerr := local.KVGet(splitKVKey); kerr != nil {
		return SplitStats{}, false, fmt.Errorf("split: kv get %s: %w", splitKVKey, kerr)
	} else if ok {
		return SplitStats{}, false, nil
	}

	stats = SplitStats{DoneAt: now}
	have, err := anySplitCandidate(d)
	if err != nil {
		return SplitStats{}, false, err
	}
	if !have {
		return stats, true, putSplitStats(local, stats)
	}

	backupPath := filepath.Join(backupDir, "relevo.db.pre-split-"+now.UTC().Format("20060102-150405"))
	if berr := d.BackupTo(backupPath); berr != nil {
		return SplitStats{}, false, berr
	}
	stats.BackupPath = backupPath

	for _, tbl := range splitTables() {
		if herr := splitBeforeTable(tbl.name); herr != nil {
			return SplitStats{}, false, herr
		}
		moved, merr := splitMoveTable(d, local, tbl)
		if merr != nil {
			return SplitStats{}, false, merr
		}
		stats.RowsMoved += moved
	}
	moved, merr := splitMoveKeys(d, local)
	if merr != nil {
		return SplitStats{}, false, merr
	}
	stats.KVKeysMoved = moved

	if err := putSplitStats(local, stats); err != nil {
		return SplitStats{}, false, err
	}
	return stats, true, nil
}

// anySplitCandidate reports whether the shared file holds a local row worth
// moving. The schema-seeded singletons do not count: every database carries
// them, so counting one would make a fresh install take a backup to move
// nothing.
func anySplitCandidate(d *DB) (bool, error) {
	for _, tbl := range splitTables() {
		if tbl.singleton {
			continue
		}
		var n int
		query := "SELECT COUNT(*) FROM " + tbl.name
		if err := d.sqlDB.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
			return false, fmt.Errorf("split: %s candidates: %w", tbl.name, mapBusy(err))
		}
		if n > 0 {
			return true, nil
		}
	}
	keys, err := d.KVKeys("")
	if err != nil {
		return false, err
	}
	for _, key := range keys {
		if splitKeyFile(key) == splitLocal {
			return true, nil
		}
	}
	return false, nil
}

// splitMoveTable copies one table's rows into the local file and then deletes
// them from the shared one. The copy is committed first, so a failure between
// the two leaves the rows in both files and the retry's insert-or-replace
// converges on one.
func splitMoveTable(shared, local *DB, tbl splitTable) (int64, error) {
	rows, err := splitReadRows(shared, tbl)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := local.Tx(func(t *Tx) error { return splitInsertRows(t, tbl, rows) }); err != nil {
		return 0, fmt.Errorf("split: %s into the local file: %w", tbl.name, mapBusy(err))
	}
	if err := shared.Tx(func(t *Tx) error { return splitDeleteRows(t, tbl, rows) }); err != nil {
		return 0, fmt.Errorf("split: %s out of the shared file: %w", tbl.name, mapBusy(err))
	}
	return int64(len(rows)), nil
}

// splitReadRows reads every row of one table into column-ordered values.
func splitReadRows(d *DB, tbl splitTable) ([][]any, error) {
	query := "SELECT " + strings.Join(tbl.columns, ", ") + " FROM " + tbl.name
	rows, err := d.sqlDB.QueryContext(context.Background(), query)
	if err != nil {
		return nil, fmt.Errorf("split: %s read: %w", tbl.name, mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	var out [][]any
	for rows.Next() {
		values := make([]any, len(tbl.columns))
		dest := make([]any, len(tbl.columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if serr := rows.Scan(dest...); serr != nil {
			return nil, fmt.Errorf("split: %s scan: %w", tbl.name, serr)
		}
		out = append(out, values)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("split: %s read: %w", tbl.name, mapBusy(rerr))
	}
	return out, nil
}

// splitInsertRows upserts the moved rows into the local file's table. Replace
// rather than insert, so a retry after a partial pass converges.
func splitInsertRows(t *Tx, tbl splitTable, rows [][]any) error {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(tbl.columns)), ",")
	query := "INSERT OR REPLACE INTO " + tbl.name + " (" + strings.Join(tbl.columns, ", ") + ") VALUES (" + marks + ")"
	for _, row := range rows {
		if _, err := t.exec(query, row...); err != nil {
			return err
		}
	}
	return nil
}

// splitDeleteRows removes the moved rows from the shared file, one statement
// per row so a row that moved under the pass is not deleted by it.
func splitDeleteRows(t *Tx, tbl splitTable, rows [][]any) error {
	where := splitKeyWhere(tbl)
	for _, row := range rows {
		args := make([]any, len(tbl.key))
		for i, col := range tbl.key {
			args[i] = row[col]
		}
		if _, err := t.exec("DELETE FROM "+tbl.name+" WHERE "+where, args...); err != nil {
			return err
		}
	}
	return nil
}

// splitKeyWhere names the natural key columns as an equality chain.
func splitKeyWhere(tbl splitTable) string {
	parts := make([]string, len(tbl.key))
	for i, col := range tbl.key {
		parts[i] = tbl.columns[col] + " = ?"
	}
	return strings.Join(parts, " AND ")
}

// splitMoveKeys moves the kv rows the allowlist routes to the local file,
// reading the value out of the shared file before deleting it there.
func splitMoveKeys(shared, local *DB) (int64, error) {
	keys, err := shared.KVKeys("")
	if err != nil {
		return 0, err
	}
	var moved int64
	for _, key := range keys {
		if splitKeyFile(key) != splitLocal {
			continue
		}
		value, ok, gerr := shared.KVGet(key)
		if gerr != nil {
			return moved, gerr
		}
		if !ok {
			continue
		}
		if perr := local.KVPut(key, value); perr != nil {
			return moved, fmt.Errorf("split: kv %s into the local file: %w", key, perr)
		}
		if derr := shared.KVDelete(key); derr != nil {
			return moved, fmt.Errorf("split: kv %s out of the shared file: %w", key, derr)
		}
		moved++
	}
	return moved, nil
}

func putSplitStats(local *DB, stats SplitStats) error {
	value, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("split: marshal stats: %w", err)
	}
	if err := local.KVPut(splitKVKey, value); err != nil {
		return fmt.Errorf("split: kv put %s: %w", splitKVKey, err)
	}
	return nil
}
