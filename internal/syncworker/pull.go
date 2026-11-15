package syncworker

import (
	"context"
	"fmt"
)

// pullPageSize bounds one pull's read per origin, in entries. The importer
// calls Pull once per run, so a backlog larger than a page drains one page per
// sync tick, and an origin's log far larger than a page is read in bounded
// fetches rather than one result holding every other origin's history. A page
// is segments, not rows: the read fetches them in groups of blobBatchWorkers
// and stops after the group that fills the page, so a page may overshoot by
// the entries of up to three segments. A legacy segment can end inside a
// batch; the read completes that batch rather than hand back half of it.
const pullPageSize = 1024

// readEntries reads every other origin's entries past that origin's mark from
// the segments its heads row names, one page of segments per origin in origin
// then sequence order. A page per origin keeps one that fills its page from
// crowding out the rest, and the mark narrows each origin's listing, so
// mostly-applied history is not listed whole and filtered here.
func (b *TursoBackend) readEntries(ctx context.Context, marks map[string]int) ([]Entry, error) {
	heads, err := b.readOtherHeads(ctx)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, h := range heads {
		if h.seq <= marks[h.origin] {
			continue
		}
		page, err := b.readOrigin(ctx, h, marks[h.origin])
		if err != nil {
			return nil, err
		}
		entries = append(entries, page...)
	}
	return pastMarks(entries, marks), nil
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

// pastMarks drops the rows a mark already covers. Sequences are cut as the
// segments are read; what remains is the batch guard: a row whose batch is at
// or below its origin's mark belongs to a batch the mark's tail covers, and
// handing back part of it would write an order the exporter never produced.
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

// PullRange reads one page of an origin's entries in (after, upto].
func (b *TursoBackend) PullRange(origin string, after, upto int) ([]Entry, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	return b.scanEntries(context.Background(),
		"SELECT origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq FROM log"+
			" WHERE origin = ? AND seq > ? AND seq <= ? ORDER BY seq LIMIT ?",
		[]any{origin, after, upto, b.page})
}

// pullRange answers with one page of an origin's entries between two sequences.
func (s *server) pullRange(req Request) Response {
	if req.Origin == "" || req.AfterSeq < 0 || req.Upto <= req.AfterSeq {
		return refuse(req, fmt.Errorf("%w: pull_range of %q over (%d, %d]", ErrProtocol, req.Origin, req.AfterSeq, req.Upto))
	}
	entries, err := s.backend.PullRange(req.Origin, req.AfterSeq, req.Upto)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Entries: entries}
}
