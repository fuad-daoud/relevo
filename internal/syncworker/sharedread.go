package syncworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// sharedColumns names the shared table's columns in the order scanSharedRows
// scans them. The list agrees with createStatements in format.go and insertRow
// in append.go.
const sharedColumns = "origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq"

// scanSharedRows scans shared table rows into entries. A delete carries no
// body and scans as nil; NULL mac, after_origin and after_seq are omitted as
// absent. at was stored as RFC3339Nano UTC, and is parsed and kept UTC.
func scanSharedRows(rows *sql.Rows) ([]Entry, error) {
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var (
			e           Entry
			body        sql.NullString
			atStr       string
			afterOrigin sql.NullString
			afterSeq    sql.NullInt64
		)
		if err := rows.Scan(
			&e.Origin, &e.Seq, &e.Batch,
			&e.Tbl, &e.PK, &e.Op,
			&e.SchemaVersion, &body, &atStr, &e.MAC,
			&afterOrigin, &afterSeq,
		); err != nil {
			return nil, MarkRefusal(CodeRemote, fmt.Errorf("syncworker: scan shared: %w", err))
		}
		if body.Valid {
			e.Body = json.RawMessage(body.String)
		}
		if afterOrigin.Valid {
			e.AfterOrigin = afterOrigin.String
		}
		if afterSeq.Valid {
			e.AfterSeq = int(afterSeq.Int64)
		}
		at, err := time.Parse(time.RFC3339Nano, atStr)
		if err != nil {
			return nil, MarkRefusal(CodeRemote, fmt.Errorf("syncworker: parse shared at %q: %w", atStr, err))
		}
		e.At = at.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, MarkRefusal(CodeRemote, fmt.Errorf("syncworker: read shared: %w", err))
	}
	if out == nil {
		return []Entry{}, nil
	}
	return out, nil
}

// sharedPull returns one page per origin of every other origin's mirrored
// entries past the mark given for it. Paging cuts at b.page rows and finishes
// the last row's batch, so a page never ends inside an atomic batch. An origin
// with no mirrored rows answers nothing, which is not an error.
func (b *TursoBackend) sharedPull(ctx context.Context, marks map[string]int) ([]Entry, error) {
	heads, err := b.readOtherHeads(ctx)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, h := range heads {
		mark := marks[h.origin]
		if h.seq <= mark {
			continue
		}
		page, err := b.readSharedOrigin(ctx, h.origin, mark)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page...)
	}
	res := pastMarks(entries, marks)
	if res == nil {
		return []Entry{}, nil
	}
	return res, nil
}

// readSharedOrigin reads one origin's mirrored entries past mark. It fetches at
// most b.page rows, then appends the remainder of the last row's batch if the
// cut fell inside one.
func (b *TursoBackend) readSharedOrigin(ctx context.Context, origin string, mark int) ([]Entry, error) {
	limit := b.page
	if limit <= 0 {
		limit = pullPageSize
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+sharedColumns+" FROM shared WHERE origin = ? AND seq > ? ORDER BY seq LIMIT ?",
		origin, mark, limit)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read shared for %s: %w", origin, err)
	}
	page, err := scanSharedRows(rows)
	if err != nil {
		return nil, err
	}
	if len(page) < limit {
		return page, nil
	}
	last := page[len(page)-1]
	restRows, err := b.db.QueryContext(ctx,
		"SELECT "+sharedColumns+" FROM shared WHERE origin = ? AND batch = ? AND seq > ? ORDER BY seq",
		origin, last.Batch, last.Seq)
	if err != nil {
		return nil, fmt.Errorf("syncworker: complete shared batch for %s: %w", origin, err)
	}
	rest, err := scanSharedRows(restRows)
	if err != nil {
		return nil, err
	}
	return append(page, rest...), nil
}

// sharedRange reads one page of origin's mirrored entries with after < seq <=
// upto, ordered by sequence, without reading heads or requiring a store.
func (b *TursoBackend) sharedRange(ctx context.Context, origin string, after, upto int) ([]Entry, error) {
	if after >= upto {
		return []Entry{}, nil
	}
	limit := b.page
	if limit <= 0 {
		limit = pullPageSize
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+sharedColumns+" FROM shared WHERE origin = ? AND seq > ? AND seq <= ? ORDER BY seq LIMIT ?",
		origin, after, upto, limit)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read shared range for %s: %w", origin, err)
	}
	return scanSharedRows(rows)
}
