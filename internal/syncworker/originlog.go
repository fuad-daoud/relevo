package syncworker

// The pull's read side: given each other origin's heads row and the mark the
// reader holds, list the segments past the mark, fetch them, and hand back
// whole batches. The bucket's segments are the log's history here; the log
// table is written beside them but not read.

import (
	"bytes"
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// maxSegmentSeq is the largest sequence a segment key can carry. Past it
// SegmentKey panics, so a heads row naming more is refused before any key is
// built from it.
const maxSegmentSeq = 999999999999

// originHead is one origin's heads row as the pull reads it: the newest
// sequence, the key of the segment holding it, and the last sequence the
// legacy migration covered.
type originHead struct {
	origin, key     string
	seq, legacyUpto int
}

// readOtherHeads reads every other origin's head in origin order. An origin
// with no row or a zero seq has written nothing to pull.
func (b *TursoBackend) readOtherHeads(ctx context.Context) ([]originHead, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT origin, seq, key, legacy_upto FROM heads WHERE origin <> ? AND seq > 0 ORDER BY origin", b.spec.Origin)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read heads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []originHead
	for rows.Next() {
		var h originHead
		if err := rows.Scan(&h.origin, &h.seq, &h.key, &h.legacyUpto); err != nil {
			return nil, fmt.Errorf("syncworker: read heads: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: read heads: %w", err)
	}
	return out, nil
}

// listStartAfter is the key a listing of one origin's segments starts after.
// While the mark is inside the legacy range the whole prefix is listed: legacy
// seqs are sparse, so no arithmetic can name the segment holding the mark's
// successor. Past it every segment is contiguous and holds at most 256
// entries, so the one ending at the mark starts no earlier than mark-255; the
// key is exclusive, so that segment is skipped and the one holding mark+1
// (first at least mark-254) is listed.
func listStartAfter(origin string, after, legacyUpto int) string {
	if after < legacyUpto {
		return ""
	}
	return blobstore.SegmentKey(origin, max(after-255, 0))
}

// selectSegmentKeys keeps the segment keys a read past after needs, in key
// order: only keys ParseSegmentKey accepts for origin (a stray object under
// the prefix is ignored), none whose first is past upper (orphans of a lost
// compare-and-set, and the ceiling a range read passes), and from the last key
// whose first is at or below after+1 -- a superset of the segment holding
// after+1 that stays correct for sparse legacy seqs, since earlier segments
// end below that key's first. With no such key, all are kept. objs arrives in
// key order, as ListAfter returns it.
func selectSegmentKeys(objs []blobstore.Object, origin string, after, upper int) []string {
	var keys []string
	start := 0
	for _, obj := range objs {
		keyOrigin, first, ok := blobstore.ParseSegmentKey(obj.Key)
		if !ok || keyOrigin != origin || first > upper {
			continue
		}
		keys = append(keys, obj.Key)
		if first <= after+1 {
			start = len(keys) - 1
		}
	}
	return keys[start:]
}

// segmentKeys lists one origin's segments past after once and selects the keys
// the read needs. One listing per call: the selection narrows it, not a second
// round trip.
func (b *TursoBackend) segmentKeys(ctx context.Context, h originHead, after, upper int) ([]string, error) {
	objs, err := b.blobs.ListAfter(ctx, h.origin+"/log/", listStartAfter(h.origin, after, h.legacyUpto))
	if err != nil {
		return nil, fmt.Errorf("syncworker: list %s's segments: %w", h.origin, err)
	}
	return selectSegmentKeys(objs, h.origin, after, upper), nil
}

// fetchedSegment is one decoded segment as the read holds it: the key it was
// fetched under, its header, and its entries.
type fetchedSegment struct {
	key     string
	header  SegmentHeader
	entries []Entry
}

// fetchSegments GETs each key with at most blobBatchWorkers in flight, decodes
// each object, and returns the segments in key order. A failed GET is
// classified as blobFetchError does; a decode failure is a remote refusal, the
// class adoptOrphan marks it with. The first error in key order is the one
// returned. The bytes are tallied after the workers join, never from them, so
// the counter reads an exact total rather than a sum still being written to.
func (b *TursoBackend) fetchSegments(ctx context.Context, keys []string) ([]fetchedSegment, error) {
	out := make([]fetchedSegment, len(keys))
	errs := make([]error, len(keys))
	sizes := make([]int64, len(keys))
	fanOut(len(keys), func(slot int) {
		key := keys[slot]
		var buf bytes.Buffer
		got, err := b.blobs.Get(ctx, key, &capWriter{w: &buf, left: segmentMaxEncoded})
		if err != nil {
			errs[slot] = blobFetchError(key, err)
			return
		}
		sizes[slot] = got
		header, entries, err := DecodeSegment(key, buf.Bytes())
		if err != nil {
			errs[slot] = MarkRefusal(CodeRemote, err)
			return
		}
		out[slot] = fetchedSegment{key: key, header: header, entries: entries}
	})
	var total int64
	for _, n := range sizes {
		total += n
	}
	b.bytes.R2Get += total
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readOrigin reads one origin's entries past mark from the segments its heads
// row names, in sequence order, stopping after the group of segments that
// fills a page.
func (b *TursoBackend) readOrigin(ctx context.Context, h originHead, mark int) ([]Entry, error) {
	// Both guards run before any SegmentKey call, which panics on an origin or
	// a sequence it cannot key. No installation can have written an origin
	// that fails CheckOrigin, so its head is skipped and the rest still read.
	if blobstore.CheckOrigin(h.origin) != nil {
		return nil, nil
	}
	if h.seq > maxSegmentSeq {
		return nil, MarkRefusal(CodeRemote, fmt.Errorf("syncworker: heads for %s name seq %d, past the segment key range", h.origin, h.seq))
	}
	keys, err := b.segmentKeys(ctx, h, mark, h.seq)
	if err != nil {
		return nil, err
	}
	// A head past the mark with no segment to read would stall the origin
	// silently: there is nothing to hand the importer to report a gap on, so
	// the segment the heads row names is reported missing instead.
	if len(keys) == 0 && h.seq > mark {
		return nil, MarkRefusal(CodeBlobMissing,
			fmt.Errorf("syncworker: heads for %s name segment %s, but none is listed past the mark", h.origin, h.key))
	}
	var entries []Entry
	var last fetchedSegment
	end := 0
	for end < len(keys) {
		groupEnd := min(end+blobBatchWorkers, len(keys))
		group, err := b.fetchSegments(ctx, keys[end:groupEnd])
		if err != nil {
			return nil, err
		}
		for _, seg := range group {
			entries = appendPastMark(entries, seg.entries, mark)
			last = seg
		}
		end = groupEnd
		if len(entries) >= b.page {
			break
		}
	}
	return b.completeLastBatch(ctx, h, keys[end:], last, entries)
}

// appendPastMark appends the entries past mark, their times normalised to UTC:
// a segment stores the writer's own zone, and the MAC covers the UTC form.
func appendPastMark(entries, page []Entry, mark int) []Entry {
	for _, e := range page {
		if e.Seq <= mark {
			continue
		}
		e.At = e.At.UTC()
		entries = append(entries, e)
	}
	return entries
}

// completeLastBatch finishes a batch a legacy segment ended inside: a batch
// applies whole and the mark moves to its tail, so half of one would let the
// next pull drop the other half. Only a legacy segment can cut a batch, and
// only while its last entry is below the head. The following keys -- the
// validated selection, in key order -- are fetched one at a time and their
// leading entries of the cut batch taken, until a segment does not continue
// it. A key past legacyUpto is a new segment, whose batch starts at its own
// first sequence and cannot continue the cut one, so it costs no GET.
func (b *TursoBackend) completeLastBatch(ctx context.Context, h originHead, keys []string, last fetchedSegment, entries []Entry) ([]Entry, error) {
	if !last.header.Legacy {
		return entries, nil
	}
	tail := last.entries[len(last.entries)-1]
	if tail.Seq >= h.seq {
		return entries, nil
	}
	for _, key := range keys {
		_, first, _ := blobstore.ParseSegmentKey(key)
		if first > h.legacyUpto {
			break
		}
		group, err := b.fetchSegments(ctx, []string{key})
		if err != nil {
			return nil, err
		}
		seg := group[0]
		matched := 0
		for _, e := range seg.entries {
			if e.Batch != tail.Batch {
				break
			}
			matched++
			e.At = e.At.UTC()
			entries = append(entries, e)
		}
		// A segment only partly of the cut batch ends it: batches never
		// interleave, so no later segment can continue it either.
		if matched != len(seg.entries) {
			break
		}
	}
	return entries, nil
}
