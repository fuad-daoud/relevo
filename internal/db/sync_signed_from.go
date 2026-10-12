package db

import "fmt"

// SignedFroms returns, per origin, the lowest sequence this file has verified a
// signed entry at. A file whose schema predates the table has none.
func (d *DB) SignedFroms() (map[string]int, error) {
	rows, err := d.sqlDB.Query(`SELECT origin, seq FROM sync_signed_from`)
	if err != nil {
		if isMissingTable(err) {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("db: signed-from records: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int{}
	for rows.Next() {
		var origin string
		var seq int
		if err := rows.Scan(&origin, &seq); err != nil {
			return nil, fmt.Errorf("db: signed-from records: %w", mapBusy(err))
		}
		out[origin] = seq
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: signed-from records: %w", mapBusy(err))
	}
	return out, nil
}

// LowerSignedFrom records seq for origin, only ever lowering an existing record.
func (d *DB) LowerSignedFrom(origin string, seq int) error {
	return d.Tx(func(t *Tx) error { return t.LowerSignedFrom(origin, seq) })
}

// LowerSignedFrom is the in-transaction form; the guard is in the statement so
// every caller gets it.
func (t *Tx) LowerSignedFrom(origin string, seq int) error {
	if _, err := t.exec(`INSERT INTO sync_signed_from (origin, seq) VALUES (?, ?)
		ON CONFLICT(origin) DO UPDATE SET seq = excluded.seq
		WHERE excluded.seq < sync_signed_from.seq`, origin, seq); err != nil {
		return fmt.Errorf("db: lower signed-from %s: %w", origin, mapBusy(err))
	}
	return nil
}
