package syncworker

// The head's read side: reconcile derives an origin's newest entry per row
// from the segments its heads row names, the same history the pull reads. The
// path is rare and own-origin, so each page re-walks the tail from the cursor
// rather than holding a cache.

import (
	"context"
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// headKey names one row of a table, the unit the head keeps a latest state for.
type headKey struct{ tbl, pk string }

// headState is one row's newest entry as the derivation keeps it: the sequence
// that wrote it, whether it deletes the row, and the body's digest -- or the
// refusal hashing it, held until the row's final state is known. No body is
// kept, so a page holds digests rather than the history they came from.
type headState struct {
	seq     int
	deleted bool
	hash    string
	hashErr error
}

// deriveHead walks the origin's segments past start in sequence order and
// returns each row's newest entry, ascending by sequence, cut to the limit
// after the delete filter.
func (b *TursoBackend) deriveHead(ctx context.Context, h originHead, start, limit int) ([]HeadRow, error) {
	// Both guards run before any SegmentKey call, which panics on an origin or
	// a sequence it cannot key. No installation can have written an origin
	// that fails CheckOrigin, so its head answers empty.
	if blobstore.CheckOrigin(h.origin) != nil {
		return []HeadRow{}, nil
	}
	if h.seq > maxSegmentSeq {
		return nil, MarkRefusal(CodeRemote, fmt.Errorf("syncworker: heads for %s name seq %d, past the segment key range", h.origin, h.seq))
	}
	// A cursor at or past the head has nothing past it to read; returning here
	// also keeps the listing's key arithmetic inside the segment key range.
	if start >= h.seq {
		return []HeadRow{}, nil
	}
	keys, err := b.segmentKeys(ctx, h, start, h.seq)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 && h.seq > start {
		return nil, missingHeadSegment(h)
	}
	state := make(map[headKey]headState)
	for end := 0; end < len(keys); {
		groupEnd := min(end+blobBatchWorkers, len(keys))
		group, err := b.fetchSegments(ctx, keys[end:groupEnd])
		if err != nil {
			return nil, err
		}
		for _, seg := range group {
			keepNewest(state, seg.entries, start)
		}
		end = groupEnd
	}
	return headRows(state, limit)
}

// keepNewest keeps each row's newest entry past start. Entries arrive in
// sequence order, so each one overwrites the row's state. A hash failure is
// stored rather than returned: a bad body a later entry supersedes is not the
// row's state, and only a survivor's failure is a refusal.
func keepNewest(state map[headKey]headState, entries []Entry, start int) {
	for _, e := range entries {
		if e.Seq <= start {
			continue
		}
		st := headState{seq: e.Seq, deleted: e.Op == opDelete}
		if e.Op == opUpsert {
			hash, err := bodyHash(e.Body)
			if err != nil {
				st.hashErr = fmt.Errorf("syncworker: hash %s %s: %w", e.Tbl, e.PK, err)
			} else {
				st.hash = hash
			}
		}
		state[headKey{tbl: e.Tbl, pk: e.PK}] = st
	}
}

// headRows collects the surviving rows in sequence order and refuses the first
// whose body could not be hashed. The limit applies after both filters, so a
// superseded or deleted row inside the range cannot end the walk early.
func headRows(state map[headKey]headState, limit int) ([]HeadRow, error) {
	keys := make([]headKey, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return state[keys[i]].seq < state[keys[j]].seq })
	var out []HeadRow
	for _, key := range keys {
		st := state[key]
		if st.deleted {
			continue
		}
		if st.hashErr != nil {
			return nil, st.hashErr
		}
		out = append(out, HeadRow{Tbl: key.tbl, PK: key.pk, Seq: st.seq, Hash: st.hash})
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
