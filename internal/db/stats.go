package db

import (
	"database/sql"
	"fmt"
	"time"
)

var statsTables = []string{"repo", "mastermind", "binding", "round", "event", "artifact", "transcript", "ingest_cursor"}

// Stats summarises row counts per table, the db's on-disk size, its schema
// version and the newest round's started_at.
func (d *DB) Stats() (Stats, error) {
	rowsMap := make(map[string]int, len(statsTables))
	for _, tbl := range statsTables {
		var n int
		if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			return Stats{}, fmt.Errorf("db: stats: count %s: %w", tbl, err)
		}
		rowsMap[tbl] = n
	}

	version, err := d.Version()
	if err != nil {
		return Stats{}, fmt.Errorf("db: stats: %w", err)
	}

	var pageCount, pageSize int64
	if err := d.sqlDB.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		return Stats{}, fmt.Errorf("db: stats: page_count: %w", err)
	}
	if err := d.sqlDB.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return Stats{}, fmt.Errorf("db: stats: page_size: %w", err)
	}

	var newest sql.Null[string]
	if err := d.sqlDB.QueryRow(`SELECT MAX(started_at) FROM round`).Scan(&newest); err != nil {
		return Stats{}, fmt.Errorf("db: stats: newest round: %w", err)
	}
	var newestRound *time.Time
	if newest.Valid {
		nt, err := parseTime(newest.V)
		if err != nil {
			return Stats{}, fmt.Errorf("db: stats: parse newest round: %w", err)
		}
		newestRound = &nt
	}

	return Stats{
		Version:     version,
		SizeBytes:   pageCount * pageSize,
		Rows:        rowsMap,
		NewestRound: newestRound,
	}, nil
}
