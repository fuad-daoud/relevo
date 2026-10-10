package synclog

// The snapshot path reads an origin's log up to its compaction floor.
//
// Compaction leaves, at or below the floor, exactly the origin's state as of the
// floor: the latest entry per row, deletes included. That is a state and not a
// history, so it cannot be applied batch by batch the way a live log is -- a
// child's entry can sit before its parent's. The region is read whole, reduced
// to one entry per row and applied parents-first.
//
// A reader joining from zero has a mark of 0 and one that fell silent past the
// mark window has a mark below the floor; both take this path, so there is no
// separate re-import.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
)

// snapshotChunkEntries bounds one transaction of a snapshot by entry count; the
// blob budget bounds it by bytes.
const snapshotChunkEntries = 200

// snapshots takes the snapshot path for every origin whose floor is past this
// machine's mark. Origins it could not finish are added to held, so the normal
// path leaves them alone for the run.
func (i *Importer) snapshots(floors, marks map[string]int, total *ImportResult, held map[string]bool) error {
	origins := make([]string, 0, len(floors))
	for origin := range floors {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	for _, origin := range origins {
		floor := floors[origin]
		if origin == i.db.Origin() || floor <= marks[origin] {
			continue
		}
		hold, err := i.snapshotOrigin(origin, marks[origin], floor, marks, total)
		var stalled *stallError
		switch {
		case errors.As(err, &stalled):
			total.Stalled = append(total.Stalled, Stall{Origin: origin, Label: i.labelOf(origin), Reason: err.Error()})
			held[origin] = true
		case err != nil:
			return err
		case hold != nil:
			total.Held = append(total.Held, *hold)
			held[origin] = true
		}
	}
	return nil
}

// snapshotOrigin brings one origin from mark to floor.
//
// The mark stays where it was until the whole region is applied. A crash in the
// middle re-applies the region, which is harmless: an upsert is idempotent and a
// delete of an absent row is a no-op.
func (i *Importer) snapshotOrigin(origin string, mark, floor int, marks map[string]int, total *ImportResult) (*Hold, error) {
	region, err := i.pullRegion(origin, mark, floor)
	if err != nil {
		return nil, fmt.Errorf("synclog: import: pull %s up to its floor: %w", origin, err)
	}
	if e, held := i.held(region); held {
		return &Hold{Origin: origin, Label: i.labelOf(origin), Seq: mark, Schema: e.SchemaVersion}, nil
	}
	upserts, deletes := splitRegion(region, total)
	if err := i.applyUntilDone(origin, upserts, total); err != nil {
		return nil, err
	}
	if err := i.applyUntilDone(origin, deletes, total); err != nil {
		return nil, err
	}
	if err := i.db.Tx(func(tx *db.Tx) error { return i.mark(tx, marks, origin, floor, true) }); err != nil {
		return nil, fmt.Errorf("synclog: import: %w", err)
	}
	total.Snapshots++
	return nil, nil
}

// pullRegion reads (after, upto] of one origin and keeps the highest sequence per
// row. A compacted log holds one entry per row up to its floor already, apart
// from the moment between a compaction's push and this pull, which the reduction
// makes harmless.
//
// The transport is not trusted for the range: an entry outside it, or from
// another origin, is ignored, and a page that does not advance ends the read.
func (i *Importer) pullRegion(origin string, after, upto int) ([]Entry, error) {
	latest := make(map[rowKey]Entry)
	cursor := after
	for cursor < upto {
		page, err := i.transport.PullRange(origin, cursor, upto)
		if err != nil {
			return nil, err
		}
		advanced := cursor
		for _, e := range page {
			if e.Origin != origin || e.Seq <= after || e.Seq > upto {
				continue
			}
			advanced = max(advanced, e.Seq)
			k := rowKey{table: e.Table, pk: e.PK}
			if prev, ok := latest[k]; !ok || e.Seq > prev.Seq {
				latest[k] = e
			}
		}
		if advanced == cursor {
			break
		}
		cursor = advanced
	}
	out := make([]Entry, 0, len(latest))
	for _, e := range latest {
		out = append(out, e)
	}
	return out, nil
}

// splitRegion orders a region for applying: upserts parents-first and by
// ascending sequence within a table, then deletes children-first. An entry for a
// table this build does not share is dropped and reported rather than applied.
func splitRegion(region []Entry, total *ImportResult) (upserts, deletes []Entry) {
	for _, e := range region {
		if _, ok := SharedTable(e.Table); !ok {
			total.Dropped = append(total.Dropped, Drop{Origin: e.Origin, Seq: e.Seq,
				Reason: fmt.Sprintf("%s %s: %q is not a shared table", e.Table, e.PK, e.Table)})
			continue
		}
		if e.Op == OpDelete {
			deletes = append(deletes, e)
		} else {
			upserts = append(upserts, e)
		}
	}
	sort.Slice(upserts, func(a, b int) bool {
		if ta, tb := tableIndex(upserts[a].Table), tableIndex(upserts[b].Table); ta != tb {
			return ta < tb
		}
		return upserts[a].Seq < upserts[b].Seq
	})
	sort.Slice(deletes, func(a, b int) bool {
		if ta, tb := tableIndex(deletes[a].Table), tableIndex(deletes[b].Table); ta != tb {
			return ta > tb
		}
		return deletes[a].Seq < deletes[b].Seq
	})
	return upserts, deletes
}

// applyUntilDone applies entries in passes. A foreign key can refuse an upsert
// whose parent has a higher sequence -- a table that references itself, as forks
// do -- so a refused entry is set aside and retried after the rest. Passes
// repeat while each applies something; one that applies nothing is a stall.
func (i *Importer) applyUntilDone(origin string, entries []Entry, total *ImportResult) error {
	for len(entries) > 0 {
		next, applied, err := i.applyPass(origin, entries, total)
		if err != nil {
			return err
		}
		if len(next) > 0 && applied == 0 {
			return &stallError{origin: origin, cause: fmt.Errorf("the references of %s refuse every remaining row", describeRows(next))}
		}
		entries = next
	}
	return nil
}

// describeRows names the first few rows, enough to find them.
func describeRows(entries []Entry) string {
	var names []string
	for n, e := range entries {
		if n == 3 {
			names = append(names, fmt.Sprintf("and %d more", len(entries)-n))
			break
		}
		names = append(names, e.Table+" "+e.PK)
	}
	return strings.Join(names, ", ")
}

// applyPass applies entries chunk by chunk and returns those a constraint refused.
func (i *Importer) applyPass(origin string, entries []Entry, total *ImportResult) (deferred []Entry, applied int, err error) {
	for _, chunk := range i.chunks(entries) {
		n, refused, err := i.applyChunk(origin, chunk, total)
		if err != nil {
			return nil, 0, err
		}
		applied += n
		deferred = append(deferred, refused...)
		i.batchDone()
	}
	total.Applied += applied
	return deferred, applied, nil
}

// chunks cuts entries so no transaction holds more than snapshotChunkEntries, or
// more declared body bytes than one batch may, which is the budget resolveRefs
// enforces.
func (i *Importer) chunks(entries []Entry) [][]Entry {
	limit := i.chunk
	if limit <= 0 {
		limit = snapshotChunkEntries
	}
	var out [][]Entry
	var cur []Entry
	var bytes int64
	for _, e := range entries {
		size := declaredBlobBytes(e)
		if len(cur) > 0 && (len(cur) >= limit || bytes+size > MaxBatchBlobBytes) {
			out = append(out, cur)
			cur, bytes = nil, 0
		}
		cur = append(cur, e)
		bytes += size
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// declaredBlobBytes is what an entry's refs say they will fetch.
func declaredBlobBytes(e Entry) int64 {
	slots, err := refSlots([]Entry{e}, make(map[int]map[string]json.RawMessage))
	if err != nil {
		return 0
	}
	var sum int64
	for _, s := range slots {
		sum += s.ref.Bytes
	}
	return sum
}
