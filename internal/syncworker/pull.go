package syncworker

import (
	"context"
	"fmt"
)

// pullPageSize bounds one read per origin, in entries. The importer calls Pull
// once per run, so a backlog larger than a page drains one page per sync tick,
// and an origin's log far larger than a page is read in bounded fetches rather
// than one result holding every other origin's history. A page is segments:
// the read fetches them in groups of blobBatchWorkers and stops after the group
// that fills the page. A pull's page may overshoot by the entries of up to
// three segments, and a legacy segment can end inside a batch the pull
// completes rather than hand back half of it; a range read cuts to the page.
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

// pastMarks drops the entries a mark already covers. Sequences are cut as the
// segments are read; what remains is the batch guard: an entry whose batch is
// at or below its origin's mark belongs to a batch the mark's tail covers, and
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

// PullRange reads one page of an origin's entries in (after, upto] from the
// segments its heads row names. It does not pull the replica first: the floor
// a caller read with ReadMarks belongs to this same replica state. The range
// is bounded by the head, never by what the bucket happens to hold, and an
// origin with no heads row answers empty without needing a store.
func (b *TursoBackend) PullRange(origin string, after, upto int) ([]Entry, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	if b.spec.History == HistoryNone || b.blobs == nil {
		return b.sharedRange(context.Background(), origin, after, upto)
	}
	ctx := context.Background()
	h, ok, err := b.readOriginHead(ctx, origin)
	if err != nil {
		return nil, err
	}
	upper := min(upto, h.seq)
	if !ok || after >= upper {
		return []Entry{}, nil
	}
	if b.blobs == nil {
		return nil, ErrNoR2
	}
	keys, err := b.segmentKeys(ctx, h, after, upper)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, missingHeadSegment(h)
	}
	var entries []Entry
	end := 0
	for end < len(keys) {
		groupEnd := min(end+blobBatchWorkers, len(keys))
		group, err := b.fetchSegments(ctx, keys[end:groupEnd])
		if err != nil {
			return nil, err
		}
		for _, seg := range group {
			entries = appendPastMark(entries, rangePrefix(seg.entries, upper), after)
		}
		end = groupEnd
		if len(entries) >= b.page {
			break
		}
	}
	if len(entries) > b.page {
		entries = entries[:b.page]
	}
	return entries, nil
}

// rangePrefix cuts a segment's entries at the range's ceiling: a segment the
// selection kept can still run past it. Entries arrive in sequence order, so
// the cut is a prefix; entries past the ceiling must not count toward the
// page, or a segment running past it could end the read before a later
// segment's in-range entries are fetched.
func rangePrefix(entries []Entry, upto int) []Entry {
	for i, e := range entries {
		if e.Seq > upto {
			return entries[:i]
		}
	}
	return entries
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
