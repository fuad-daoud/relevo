package syncworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// The migration's commit: one replica transaction builds heads and shared,
// sets format 6 and drops log and marks, then one push carries it. The
// transaction re-checks the drop rule, so a late row landing between the last
// sweep and the commit refuses instead of dropping history.

// commitMigration runs the one transaction that turns the replica from
// format 5 to 6 and pushes it. Covered is the sweep's cover, writes the last
// pass's per-origin writes.
func (b *TursoBackend) commitMigration(ctx context.Context, covered map[string]int, writes map[string]originWrite) (map[string]int, map[string]string, int, error) {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("syncworker: migrate the log: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	floors, labels, sharedRows, err := b.commitTx(ctx, tx, covered, writes)
	if err != nil {
		return nil, nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, 0, fmt.Errorf("syncworker: migrate the log: %w", err)
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return nil, nil, 0, fmt.Errorf("syncworker: push the migrated log: %w", markTransferRefusal(err))
	}
	return floors, labels, sharedRows, nil
}

// commitTx does the transaction's work: re-check the drop, then rebuild the
// tables. Heads carries the floors now, so the floor meta rows go with the
// old tables.
func (b *TursoBackend) commitTx(ctx context.Context, tx *sql.Tx, covered map[string]int, writes map[string]originWrite) (map[string]int, map[string]string, int, error) {
	logMax, err := readTxLogMaxes(ctx, tx)
	if err != nil {
		return nil, nil, 0, err
	}
	if blockers := dropBlockers(logMax, covered); len(blockers) > 0 {
		return nil, nil, 0, MarkRefusal(CodeMigrateIncomplete,
			fmt.Errorf("syncworker: rows for %s are above the migrated segments: run the migrate again to sweep them",
				strings.Join(blockers, ", ")))
	}
	floors, err := readTxFloors(ctx, tx)
	if err != nil {
		return nil, nil, 0, err
	}
	labels, err := b.readTxLabels(ctx, tx, logMax)
	if err != nil {
		return nil, nil, 0, err
	}
	if err := createTxTables(ctx, tx); err != nil {
		return nil, nil, 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := insertTxHeads(ctx, tx, now, logMax, writes, floors, labels); err != nil {
		return nil, nil, 0, err
	}
	sharedRows, err := b.insertTxShared(ctx, tx)
	if err != nil {
		return nil, nil, 0, err
	}
	if err := finishTxMigration(ctx, tx); err != nil {
		return nil, nil, 0, err
	}
	return floors, labels, sharedRows, nil
}

// readTxLogMaxes reads every origin's highest log sequence inside the commit
// transaction, so the drop check sees what the commit drops.
func readTxLogMaxes(ctx context.Context, tx *sql.Tx) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, "SELECT origin, MAX(seq) FROM log GROUP BY origin")
	if err != nil {
		return nil, fmt.Errorf("syncworker: read the log's origins: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var origin string
		var max int
		if err := rows.Scan(&origin, &max); err != nil {
			return nil, fmt.Errorf("syncworker: read the log's origins: %w", err)
		}
		out[origin] = max
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: read the log's origins: %w", err)
	}
	return out, nil
}

// readTxFloors reads the compaction floors the heads rows inherit: the value
// of each floor.<origin> meta row, 0 when absent or not a number, since the
// row is untrusted and a bad one must not stop the migration.
func readTxFloors(ctx context.Context, tx *sql.Tx) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, "SELECT key, value FROM meta WHERE key LIKE 'floor.%'")
	if err != nil {
		return nil, fmt.Errorf("syncworker: read the floors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("syncworker: read the floors: %w", err)
		}
		origin := strings.TrimPrefix(key, "floor.")
		n, err := strconv.Atoi(value)
		if err != nil {
			continue
		}
		out[origin] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: read the floors: %w", err)
	}
	return out, nil
}

// readTxLabels reads each origin's label from its newest installation upsert,
// whose pk is the json array of the origin. No row, or a body with no label,
// reads as empty, which is how an origin that never named itself migrates.
func (b *TursoBackend) readTxLabels(ctx context.Context, tx *sql.Tx, logMax map[string]int) (map[string]string, error) {
	origins := make([]string, 0, len(logMax))
	for origin := range logMax {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	out := make(map[string]string)
	for _, origin := range origins {
		pk, err := json.Marshal([]string{origin})
		if err != nil {
			return nil, fmt.Errorf("syncworker: read the label: %w", err)
		}
		var body sql.NullString
		err = tx.QueryRowContext(ctx,
			"SELECT body FROM log WHERE tbl = 'installation' AND pk = ? AND op = 'upsert' ORDER BY seq DESC LIMIT 1",
			string(pk)).Scan(&body)
		if err == sql.ErrNoRows || !body.Valid {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("syncworker: read the label: %w", err)
		}
		if label := labelOf(body.String); label != "" {
			out[origin] = label
		}
	}
	return out, nil
}

// labelOf extracts the label from an installation body, empty when the body
// carries none. A body that is not an object reads as empty rather than
// refusing the migration over one row's shape.
func labelOf(body string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return ""
	}
	raw, ok := obj["label"]
	if !ok {
		return ""
	}
	var label string
	if err := json.Unmarshal(raw, &label); err != nil {
		return ""
	}
	return label
}

// createTxTables drops the crashed run's partial tables and creates the two
// the migration builds, by name rather than by position in the schema list.
func createTxTables(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS heads",
		"DROP TABLE IF EXISTS shared",
		createHeadsStatement,
		createSharedStatement,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("syncworker: build the migrated tables: %w", err)
		}
	}
	return nil
}

// insertTxHeads writes one heads row per origin the log names: the max seq,
// the last segment's key, the rows and bytes written, the floor and label read
// above, and now. The key comes from the last object's first seq, which is
// what the pull lists by.
func insertTxHeads(ctx context.Context, tx *sql.Tx, now string, logMax map[string]int, writes map[string]originWrite, floors map[string]int, labels map[string]string) error {
	origins := make([]string, 0, len(logMax))
	for origin := range logMax {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	for _, origin := range origins {
		w, ok := writes[origin]
		if !ok || w.rows == 0 {
			return fmt.Errorf("syncworker: migrate %s without its segments", origin)
		}
		key := blobstore.SegmentKey(origin, w.lastFirst)
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO heads (origin, seq, key, entries, bytes, floor, legacy_upto, label, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			origin, logMax[origin], key, w.rows, w.bytes, floors[origin], logMax[origin], labels[origin], now); err != nil {
			return fmt.Errorf("syncworker: write the heads: %w", err)
		}
	}
	return nil
}

// insertTxShared copies the mirrored tables verbatim into shared. Both column
// lists are spelled out, so a column order change can never shift a value,
// and the filter is the hello's mirror list, which a migrate hello always
// names.
func (b *TursoBackend) insertTxShared(ctx context.Context, tx *sql.Tx) (int, error) {
	placeholders := strings.Repeat("?, ", len(b.spec.MirrorTables)-1) + "?"
	args := make([]any, 0, len(b.spec.MirrorTables))
	for _, tbl := range b.spec.MirrorTables {
		args = append(args, tbl)
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO shared (origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq)"+
			" SELECT origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq"+
			" FROM log WHERE tbl IN ("+placeholders+")", args...)
	if err != nil {
		return 0, fmt.Errorf("syncworker: write the shared rows: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("syncworker: count the shared rows: %w", err)
	}
	return int(n), nil
}

// finishTxMigration drops the floor rows heads now carries, sets format 6 and
// drops the old tables. The signed, cut, key and claim rows stay: only the
// floors move into heads and only the old tables go.
func finishTxMigration(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key LIKE 'floor.%'"); err != nil {
		return fmt.Errorf("syncworker: drop the floors: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE meta SET value = ? WHERE key = ?", "6", metaFormatKey); err != nil {
		return fmt.Errorf("syncworker: write the log format: %w", err)
	}
	for _, stmt := range []string{"DROP TABLE log", "DROP TABLE marks"} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("syncworker: drop the old tables: %w", err)
		}
	}
	return nil
}
