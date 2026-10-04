package db

import (
	"context"
	"fmt"
	"strings"
)

// originGateTables is every table carrying an origin column, in the order the
// gate names them. The list is the gate's whole scope: a table that gains an
// origin column joins it here, so a count cannot be missed by reading an older
// list somewhere else.
var originGateTables = []string{"binding_record", "binding", "repo", "mastermind", "chains"}

// OriginGateTableCount is one table's share of the gate: how many of its rows
// no installation has stamped.
type OriginGateTableCount struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// OriginGateCounts is the per-table count of unstamped rows, in
// originGateTables order so a report of the same database is the same string
// twice.
type OriginGateCounts struct {
	Tables []OriginGateTableCount `json:"tables"`
}

// Empty reports whether every table's count is zero: the gate passes.
func (c OriginGateCounts) Empty() bool {
	for _, t := range c.Tables {
		if t.Rows > 0 {
			return false
		}
	}
	return true
}

// String names each table still holding an unstamped row with its count, and
// says so plainly when none does.
func (c OriginGateCounts) String() string {
	parts := make([]string, 0, len(c.Tables))
	for _, t := range c.Tables {
		if t.Rows > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", t.Table, t.Rows))
		}
	}
	if len(parts) == 0 {
		return "no rows with an empty origin"
	}
	return strings.Join(parts, " ")
}

// CountEmptyOrigins counts, per origin-carrying table, the rows no installation
// has stamped. It only counts. Enable is a sync decision, and stamping a row
// inside it would mutate history as a side effect of that decision, so an
// unstamped row stays until the backfill or an upgrade removes it.
func CountEmptyOrigins(d *DB) (OriginGateCounts, error) {
	counts := OriginGateCounts{Tables: make([]OriginGateTableCount, 0, len(originGateTables))}
	for _, table := range originGateTables {
		var n int64
		query := `SELECT COUNT(*) FROM ` + table + ` WHERE origin = ''`
		if err := d.sqlDB.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
			return OriginGateCounts{}, fmt.Errorf("db: origin gate: %s: %w", table, mapBusy(err))
		}
		counts.Tables = append(counts.Tables, OriginGateTableCount{Table: table, Rows: n})
	}
	return counts, nil
}
