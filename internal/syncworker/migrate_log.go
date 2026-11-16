package syncworker

// The migration's read side: the format-5 log rows one origin at a time, and
// the pure rule for which origins still block the drop. The writer in
// migrate_segments.go consumes these; plan 8's Migrate calls both.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// logOriginMaxes reads every origin's highest format-5 log sequence. An empty
// log answers an empty map: an origin with no rows blocks nothing.
func (b *TursoBackend) logOriginMaxes(ctx context.Context) (map[string]int, error) {
	rows, err := b.db.QueryContext(ctx, "SELECT origin, MAX(seq) FROM log GROUP BY origin")
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

// readLogChunk reads one origin's format-5 rows past after, at most limit, in
// sequence order. It is the inverse of insertRow: a NULL body is a delete and
// reads as an empty body, a NULL mac or after column reads as none, and at is
// held as UTC because the MAC covers the UTC form. The limit bounds one page,
// so the writer pages through the log one chunk at a time.
func (b *TursoBackend) readLogChunk(ctx context.Context, origin string, after, limit int) ([]Entry, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq"+
			" FROM log WHERE origin = ? AND seq > ? ORDER BY seq LIMIT ?",
		origin, after, limit)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read the log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var e Entry
		var body, afterOrigin sql.NullString
		var at string
		var afterSeq sql.NullInt64
		if err := rows.Scan(&e.Origin, &e.Seq, &e.Batch, &e.Tbl, &e.PK, &e.Op,
			&e.SchemaVersion, &body, &at, &e.MAC, &afterOrigin, &afterSeq); err != nil {
			return nil, fmt.Errorf("syncworker: read the log: %w", err)
		}
		if body.Valid {
			e.Body = json.RawMessage(body.String)
		}
		parsed, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("syncworker: read the log: %w", err)
		}
		e.At = parsed.UTC()
		if afterOrigin.Valid {
			e.AfterOrigin = afterOrigin.String
		}
		if afterSeq.Valid {
			e.AfterSeq = int(afterSeq.Int64)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: read the log: %w", err)
	}
	return out, nil
}

// dropBlockers is the one rule for what may drop: the origins, sorted, whose
// log max exceeds what the segments cover. An origin with rows and no coverage
// counts as uncovered, so a migration that wrote nothing for it still blocks.
// An origin the log no longer names is not listed, however much cover it
// holds.
func dropBlockers(logMax, covered map[string]int) []string {
	var out []string
	for origin, max := range logMax {
		if covered[origin] < max {
			out = append(out, origin)
		}
	}
	sort.Strings(out)
	return out
}
