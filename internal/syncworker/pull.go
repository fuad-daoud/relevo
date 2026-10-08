package syncworker

import (
	"context"
	"fmt"
	"strings"
)

// pullPageSize bounds one pull's read. The importer asks again for whatever the
// page left, so an origin's log far larger than a round is read in bounded
// statements rather than one result holding every other origin's history. It
// sits above the size one append writes, so a page normally ends on a batch
// boundary; when it does not, the read completes that batch rather than hand
// back half of it.
const pullPageSize = 1024

// readEntries reads every other origin's log rows past that origin's mark, in
// origin then sequence order, bounded to one page. The mark is applied in SQL
// per origin, so a log whose history is mostly applied is not read whole and
// filtered here.
//
// A page that fills may end inside the last batch it read, so that batch is
// completed before the entries are returned. A batch is applied whole and the
// mark moves to its tail, so handing back half of one would let the next pull
// drop the other half as though the mark already covered it.
func (b *TursoBackend) readEntries(ctx context.Context, marks map[string]int) ([]Entry, error) {
	origins, err := b.otherOrigins(ctx)
	if err != nil {
		return nil, err
	}
	if len(origins) == 0 {
		return nil, nil
	}
	entries, err := b.readPage(ctx, origins, marks)
	if err != nil {
		return nil, err
	}
	if len(entries) >= b.page && len(entries) > 0 {
		last := entries[len(entries)-1]
		query, args := restOfBatchQuery(last)
		more, err := b.scanEntries(ctx, query, args)
		if err != nil {
			return nil, err
		}
		entries = append(entries, more...)
	}
	return pastMarks(entries, marks), nil
}

// otherOrigins is every origin that has written to the log but this worker's, in
// the order a pull reads them. Enumerating them is what lets the per-origin mark
// narrow the read in SQL instead of the log being read whole and filtered here.
func (b *TursoBackend) otherOrigins(ctx context.Context) ([]string, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT DISTINCT origin FROM log WHERE origin <> ? ORDER BY origin", b.spec.Origin)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read the log's origins: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var origin string
		if err := rows.Scan(&origin); err != nil {
			return nil, fmt.Errorf("syncworker: read the log's origins: %w", err)
		}
		out = append(out, origin)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: read the log's origins: %w", err)
	}
	return out, nil
}

// readPage reads one page of the pull's rows.
func (b *TursoBackend) readPage(ctx context.Context, origins []string, marks map[string]int) ([]Entry, error) {
	query, args := pullQuery(origins, marks, b.page)
	return b.scanEntries(ctx, query, args)
}

// pullQuery builds the one SELECT a pull runs: each origin's rows past that
// origin's own mark, in origin then sequence order, bounded to limit. The marks
// are bound per origin so no mark is spliced into the statement, and an origin
// with no mark is read from its first sequence.
func pullQuery(origins []string, marks map[string]int, limit int) (string, []any) {
	clauses := make([]string, 0, len(origins))
	args := make([]any, 0, len(origins)*2+1)
	for _, origin := range origins {
		clauses = append(clauses, "(origin = ? AND seq > ?)")
		args = append(args, origin, marks[origin])
	}
	query := "SELECT origin, seq, batch, tbl, pk, op, schema_version, body, at FROM log" +
		" WHERE " + strings.Join(clauses, " OR ") +
		" ORDER BY origin, seq LIMIT ?"
	return query, append(args, limit)
}

// restOfBatchQuery builds the read that completes a batch a page cut: the rows
// of one batch after the last row the page held. It is bounded to that batch, so
// a batch longer than a page is still read in one piece.
func restOfBatchQuery(last Entry) (string, []any) {
	return "SELECT origin, seq, batch, tbl, pk, op, schema_version, body, at FROM log" +
			" WHERE origin = ? AND batch = ? AND seq > ? ORDER BY seq",
		[]any{last.Origin, last.Batch, last.Seq}
}

// scanEntries runs one read and turns its rows into entries.
func (b *TursoBackend) scanEntries(ctx context.Context, query string, args []any) ([]Entry, error) {
	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read the log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: read the log: %w", err)
	}
	return out, nil
}

// pastMarks drops the rows a mark already covers. The sequences themselves are
// cut in SQL, so what remains here is the batch guard: a row whose batch is at
// or below its origin's mark belongs to a batch the mark's tail covers, and
// handing back part of that batch would write an order the exporter never
// produced.
func pastMarks(entries []Entry, marks map[string]int) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Batch <= marks[e.Origin] {
			continue
		}
		out = append(out, e)
	}
	return out
}
