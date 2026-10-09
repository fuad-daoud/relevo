package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A converge conflict is a shared stray whose local counterpart already
// holds different content: the live local row stays, the stray is converged
// away, and the pass counts and names the row for the log instead of
// overwriting. Names only, never contents: a conflicting row may be a secret.

// splitConflictCap bounds the conflict names a pass reports: the count is
// exact, the names are a sample for the log.
const splitConflictCap = 16

func (s *SplitStats) addConflict(name string) {
	s.Conflicts++
	if len(s.ConflictKeys) < splitConflictCap {
		s.ConflictKeys = append(s.ConflictKeys, name)
	}
}

// splitReadTxRow reads the local row with the moved row's natural key, for
// the conflict check. ok is false when the local file holds no such row.
func splitReadTxRow(t *Tx, tbl splitTable, row []any) ([]any, bool, error) {
	where := splitKeyWhere(tbl)
	args := make([]any, len(tbl.key))
	for i, col := range tbl.key {
		args[i] = row[col]
	}
	query := "SELECT " + strings.Join(tbl.columns, ", ") + " FROM " + tbl.name + " WHERE " + where
	values := make([]any, len(tbl.columns))
	dest := make([]any, len(tbl.columns))
	for i := range values {
		dest[i] = &values[i]
	}
	err := t.conn.QueryRowContext(t.ctx, query, args...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("split: %s local read: %w", tbl.name, mapBusy(err))
	}
	return values, true, nil
}

// splitRowsEqual compares a shared row against its local counterpart by
// content, skipping updated_at: a row the two files merely stamped at
// different times is the same row, not a conflict.
func splitRowsEqual(tbl splitTable, local, shared []any) bool {
	for i, col := range tbl.columns {
		if col == "updated_at" {
			continue
		}
		if !splitCellEqual(local[i], shared[i]) {
			return false
		}
	}
	return true
}

// splitRowKey names a conflicting row for the log: its table and natural
// key, never its contents.
func splitRowKey(tbl splitTable, row []any) string {
	parts := make([]string, len(tbl.key))
	for i, col := range tbl.key {
		parts[i], _ = splitCellText(row[col])
	}
	return strings.Join(parts, "/")
}

// splitCellEqual compares two scanned column values by content: the same
// bytes compare equal across the TEXT/BLOB boundary, so a value the two
// files merely typed differently is not a conflict.
func splitCellEqual(a, b any) bool {
	an, aok := splitCellNorm(a)
	bn, bok := splitCellNorm(b)
	return aok && bok && an == bn
}

// splitCellNorm renders a scanned column value in comparable form. A type
// the scanner should never produce compares equal to nothing, so an
// unfamiliar value takes the conflict path instead of silently converging.
func splitCellNorm(v any) (any, bool) {
	switch c := v.(type) {
	case nil:
		return nil, true
	case []byte:
		return string(c), true
	case string, bool, int64, float64, time.Time:
		return v, true
	}
	return nil, false
}

// splitCellText renders a natural-key cell for a conflict name.
func splitCellText(v any) (string, bool) {
	switch c := v.(type) {
	case nil:
		return "", true
	case []byte:
		return string(c), true
	case string:
		return c, true
	}
	return "", false
}
