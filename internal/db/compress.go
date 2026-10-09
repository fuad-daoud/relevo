package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// compressKVKey is the kv key a finished pass is recorded under. Its presence
// short-circuits later daemon starts, and it is written only after every batch,
// the checkpoint and the vacuum, so a killed or failed pass retries from the
// top on the next start. It is machine-local: a shared copy would tell another
// installation's pass that this machine's rows were already converted.
const compressKVKey = "zstd-compress.v1"

// compressBatchRows bounds one transaction's rows, keeping the write lock
// short while a pass walks a large history.
const compressBatchRows = 200

// CompressTableStats records one table's share of a pass.
type CompressTableStats struct {
	Table             string `json:"table"`
	RowsCompressed    int    `json:"rows_compressed"`
	ColumnsCompressed int    `json:"columns_compressed"`
	ColumnsKeptPlain  int    `json:"columns_kept_plain"`
	BytesIn           int64  `json:"bytes_in"`
	BytesOut          int64  `json:"bytes_out"`
}

// CompressStats records one finished pass, both in the kv row and in the
// daemon's log.
type CompressStats struct {
	DoneAt        time.Time            `json:"done_at"`
	BackupPath    string               `json:"backup_path,omitempty"`
	Tables        []CompressTableStats `json:"tables"`
	CheckpointErr string               `json:"checkpoint_err,omitempty"`
	VacuumErr     string               `json:"vacuum_err,omitempty"`
}

// compressColumn names one converted column beside the flag holding its codec.
type compressColumn struct {
	value string
	codec string
}

// compressTable names one table and the columns the pass converts in it.
type compressTable struct {
	name    string
	columns []compressColumn
}

// compressTables is the v1 scope: the columns carrying the bulk of the bytes.
func compressTables() []compressTable {
	return []compressTable{
		{name: "round_file", columns: []compressColumn{{value: "body", codec: "body_codec"}}},
		{name: "transcript", columns: []compressColumn{
			{value: "record_json", codec: "record_json_codec"},
			{value: "rendered", codec: "rendered_codec"},
		}},
	}
}

// CompressHistoryOnce converts the columns the pass scope names, once per
// database and only after a full backup. An existing kv row means an earlier
// pass finished, so ran is false. An empty candidate set writes the stats and
// returns with no backup. Otherwise the database is backed up first (a failure
// returns the error with nothing converted and no kv row), the rows are
// converted in bounded batches, and the finish step checkpoints, vacuums and
// writes the stats; a checkpoint or vacuum failure is recorded in the stats
// rather than returned, since the rows are converted either way.
func CompressHistoryOnce(d *DB, backupDir string, now time.Time) (stats CompressStats, ran bool, err error) {
	local := d.LocalOrSelf()
	if _, ok, kerr := local.KVGet(compressKVKey); kerr != nil {
		return CompressStats{}, false, fmt.Errorf("compress: kv get %s: %w", compressKVKey, kerr)
	} else if ok {
		return CompressStats{}, false, nil
	}

	tables := compressTables()
	stats = CompressStats{DoneAt: now, Tables: []CompressTableStats{}}
	have, err := anyCompressCandidate(d, tables)
	if err != nil {
		return CompressStats{}, false, err
	}
	if !have {
		if err := putCompressStats(local, stats); err != nil {
			return CompressStats{}, false, err
		}
		return stats, true, nil
	}

	backupPath := filepath.Join(backupDir, "relevo.db.pre-zstd-"+now.UTC().Format("20060102-150405"))
	if berr := d.BackupTo(backupPath); berr != nil {
		return CompressStats{}, false, berr
	}
	stats.BackupPath = backupPath

	for _, tbl := range tables {
		ts, cerr := convertTable(d, tbl)
		if cerr != nil {
			return CompressStats{}, false, cerr
		}
		stats.Tables = append(stats.Tables, ts)
	}

	stats = finishCompress(d, stats)
	if err := putCompressStats(local, stats); err != nil {
		return CompressStats{}, false, err
	}
	return stats, true, nil
}

// anyCompressCandidate reports whether any table holds a row the pass would
// inspect, so an already-converted or empty database skips the backup.
func anyCompressCandidate(d *DB, tables []compressTable) (bool, error) {
	for _, tbl := range tables {
		found, err := hasCompressCandidate(d, tbl)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func hasCompressCandidate(d *DB, tbl compressTable) (bool, error) {
	var rowid int64
	query := fmt.Sprintf(`SELECT rowid FROM %s WHERE %s LIMIT 1`, tbl.name, candidateWhere(tbl.columns))
	err := d.sqlDB.QueryRowContext(context.Background(), query).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("compress: %s candidate: %w", tbl.name, mapBusy(err))
	}
	return true, nil
}

// candidateWhere matches a row with at least one converted column that is not
// already a zstd frame. The test is `<> 1` rather than `= 0` so an unknown
// codec reaches the pass and is refused there, instead of being left behind.
func candidateWhere(columns []compressColumn) string {
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = fmt.Sprintf("%s <> %d", c.codec, codecZstd)
	}
	return strings.Join(parts, " OR ")
}

// convertTable converts every candidate row of one table, one bounded batch
// per transaction.
func convertTable(d *DB, tbl compressTable) (CompressTableStats, error) {
	ts := CompressTableStats{Table: tbl.name}
	cursor := int64(0)
	for {
		rowids, err := compressBatch(d, tbl, cursor)
		if err != nil {
			return ts, err
		}
		if len(rowids) == 0 {
			return ts, nil
		}
		if err := d.Tx(func(t *Tx) error { return compressRows(t, tbl, rowids, &ts) }); err != nil {
			return ts, err
		}
		cursor = rowids[len(rowids)-1]
	}
}

// compressBatch returns the next rowids to convert, ordered. The rowid cursor
// is what ends the loop: a row whose columns all stay plain remains a
// candidate forever, so re-querying by codec alone would never advance.
func compressBatch(d *DB, tbl compressTable, after int64) ([]int64, error) {
	query := fmt.Sprintf(`SELECT rowid FROM %s WHERE rowid > ? AND (%s) ORDER BY rowid ASC LIMIT ?`,
		tbl.name, candidateWhere(tbl.columns))
	rows, err := d.sqlDB.QueryContext(context.Background(), query, after, compressBatchRows)
	if err != nil {
		return nil, fmt.Errorf("compress: %s batch: %w", tbl.name, mapBusy(err))
	}
	ids, err := collectRows(rows, scanRowID)
	if err != nil {
		return nil, fmt.Errorf("compress: %s batch: %w", tbl.name, mapBusy(err))
	}
	return ids, nil
}

func scanRowID(s rowScanner) (int64, error) {
	var id int64
	if err := s.Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func compressRows(t *Tx, tbl compressTable, rowids []int64, ts *CompressTableStats) error {
	for _, rowid := range rowids {
		changed, err := compressRow(t, tbl, rowid, ts)
		if err != nil {
			return err
		}
		if changed {
			ts.RowsCompressed++
		}
	}
	return nil
}

// compressRow reads one row, encodes its still-plain converted columns and
// updates the row when at least one column got smaller. A column already under
// codec 1 is left alone; an unknown codec is refused rather than skipped.
func compressRow(t *Tx, tbl compressTable, rowid int64, ts *CompressTableStats) (bool, error) {
	values, codecs, err := readCompressRow(t, tbl, rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("compress: %s rowid %d: read: %w", tbl.name, rowid, err)
	}

	var (
		sets []string
		args []any
	)
	for i, col := range tbl.columns {
		switch codecs[i] {
		case codecZstd:
			continue
		case codecPlain:
			stored, codec := encodeColumn(values[i])
			ts.BytesIn += int64(len(values[i]))
			ts.BytesOut += int64(len(stored))
			if codec == codecPlain {
				ts.ColumnsKeptPlain++
				continue
			}
			ts.ColumnsCompressed++
			sets = append(sets, col.value+" = ?", col.codec+" = ?")
			args = append(args, stored, codec)
		default:
			return false, fmt.Errorf("compress: %s rowid %d: column %s: unknown codec %d: %w",
				tbl.name, rowid, col.value, codecs[i], ErrInvalid)
		}
	}
	if len(sets) == 0 {
		return false, nil
	}

	query := fmt.Sprintf(`UPDATE %s SET %s WHERE rowid = ?`, tbl.name, strings.Join(sets, ", "))
	args = append(args, rowid)
	if _, err := t.exec(query, args...); err != nil {
		return false, fmt.Errorf("compress: %s rowid %d: update: %w", tbl.name, rowid, mapBusy(err))
	}
	return true, nil
}

// readCompressRow reads one row's converted columns and their codecs. A row
// gone since the batch was planned reads as sql.ErrNoRows and is skipped.
func readCompressRow(t *Tx, tbl compressTable, rowid int64) ([][]byte, []int, error) {
	parts := make([]string, 0, len(tbl.columns)*2)
	for _, c := range tbl.columns {
		parts = append(parts, c.value, c.codec)
	}
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE rowid = ?`, strings.Join(parts, ", "), tbl.name)

	values := make([][]byte, len(tbl.columns))
	codecs := make([]int, len(tbl.columns))
	dests := make([]any, 0, len(tbl.columns)*2)
	for i := range tbl.columns {
		dests = append(dests, &values[i], &codecs[i])
	}
	if err := t.queryRow(query, rowid).Scan(dests...); err != nil {
		return nil, nil, err
	}
	return values, codecs, nil
}

// finishCompress checkpoints, vacuums and checkpoints again: a -wal left in
// place would keep the conversion's size, and the vacuum's own output needs the
// second truncate. A failure is recorded, never returned, since the rows are
// converted either way.
func finishCompress(d *DB, stats CompressStats) CompressStats {
	if err := d.walCheckpoint(); err != nil {
		stats.CheckpointErr = err.Error()
	}
	if err := d.Vacuum(); err != nil {
		stats.VacuumErr = err.Error()
	}
	if err := d.walCheckpoint(); err != nil && stats.CheckpointErr == "" {
		stats.CheckpointErr = err.Error()
	}
	return stats
}

// walCheckpoint truncates the write-ahead log, which is what lets the
// conversion shrink the database file rather than only the -wal.
func (d *DB) walCheckpoint() error {
	if _, err := d.sqlDB.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("db: wal checkpoint: %w", mapBusy(err))
	}
	return nil
}

func putCompressStats(d *DB, stats CompressStats) error {
	value, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("compress: marshal stats: %w", err)
	}
	if err := d.KVPut(compressKVKey, value); err != nil {
		return fmt.Errorf("compress: kv put %s: %w", compressKVKey, err)
	}
	return nil
}
