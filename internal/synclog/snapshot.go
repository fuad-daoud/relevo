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

// The region is held whole in memory, and the log it comes from is written by
// other machines, so it is bounded: by rows, and by the inline body bytes those
// rows carry (bodies over the ref threshold travel as refs and are fetched a
// chunk at a time). A region past either is not one this design produces.
const (
	maxRegionRows  = 1_000_000
	maxRegionBytes = int64(512) << 20
)

// maxSnapshotPasses bounds the retry passes a region takes. Each pass applies
// the rows whose references are in place, so the passes an honest region needs
// are the depth of its parent chains; a region built to need one pass per row
// would otherwise cost a pass over the whole region for every row in it.
const maxSnapshotPasses = 64

// snapshots takes the snapshot path for every origin whose floor is past this
// machine's mark. Origins it could not finish are added to held, so the normal
// path leaves them alone for the run.
func (i *Importer) snapshots(state MarkState, marks map[string]int, total *ImportResult, held map[string]bool) error {
	origins := make([]string, 0, len(state.Floors))
	for origin := range state.Floors {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	for {
		progress := false
		var lastHolds []*Hold
		for _, origin := range origins {
			floor := state.Floors[origin]
			if origin == i.db.Origin() || floor <= marks[origin] || held[origin] {
				continue
			}
			hold, err := i.snapshotOrigin(origin, marks[origin], floor, state.Heads[origin], marks, total)
			switch {
			case holdsOrigin(err):
				total.Stalled = append(total.Stalled, Stall{Origin: origin, Label: i.labelOf(origin), Reason: err.Error()})
				held[origin] = true
			case err != nil:
				return err
			case hold != nil:
				lastHolds = append(lastHolds, hold)
			default:
				progress = true
			}
		}
		if !progress {
			for _, hold := range lastHolds {
				total.Held = append(total.Held, *hold)
				held[hold.Origin] = true
			}
			break
		}
	}
	return nil
}

// snapshotOrigin brings one origin from mark to floor.
//
// The mark stays where it was until the whole region is applied. A crash in the
// middle re-applies the region, which is harmless: an upsert is idempotent and a
// delete of an absent row is a no-op.
//
// Rows whose references refuse them on every pass are dropped and the mark still
// moves to the floor, the way the normal path moves past a batch it refuses:
// left at the old mark, the region would be pulled, fetched and applied again on
// every run, and refuse the same rows each time.
func (i *Importer) snapshotOrigin(origin string, mark, floor, head int, marks map[string]int, total *ImportResult) (*Hold, error) {
	if floor > head {
		return nil, badFloor(origin, floor, fmt.Sprintf("past the log's head %d", head))
	}
	region, reached, err := i.pullRegion(origin, mark, floor)
	if err != nil {
		return nil, fmt.Errorf("synclog: import: pull %s up to its floor: %w", origin, err)
	}
	if err := i.checkFloor(origin, floor, reached); err != nil {
		return nil, err
	}
	if e, held := i.held(region); held {
		return &Hold{Origin: origin, Label: i.labelOf(origin), Seq: mark, Schema: e.SchemaVersion}, nil
	}
	if hold := i.unknownDoc(origin, mark, region); hold != nil {
		return hold, nil
	}
	if err := i.verify(origin, region); err != nil {
		var holdErr *HoldError
		if errors.As(err, &holdErr) {
			return &Hold{Origin: origin, Label: i.labelOf(origin), Seq: mark, Reason: holdErr.Reason}, nil
		}
		return nil, err
	}
	if hold, err := i.checkRegionAfterHold(origin, mark, region, marks); err != nil || hold != nil {
		return hold, err
	}
	docs, rows := splitDocs(region)
	handled, err := i.takeDocs(origin, docs)
	if err != nil && !refused(err) {
		return nil, err
	}
	if err != nil {
		total.Dropped = append(total.Dropped, Drop{Origin: origin, Seq: floor, Reason: err.Error()})
	}
	total.Applied += handled
	upserts, deletes := splitRegion(rows, total)
	for _, part := range [][]Entry{upserts, deletes} {
		left, err := i.applyUntilDone(origin, part, total)
		if err != nil {
			var holdErr *HoldError
			if errors.As(err, &holdErr) {
				return &Hold{Origin: origin, Label: i.labelOf(origin), Seq: mark, Reason: holdErr.Reason}, nil
			}
			return nil, err
		}
		if left != nil {
			total.Dropped = append(total.Dropped, Drop{Origin: origin, Seq: floor, Reason: left.Error()})
		}
	}
	var signedAt int
	var wrote bool
	if err := i.db.Tx(func(tx *db.Tx) error {
		var err error
		if signedAt, wrote, err = i.recordSigned(tx, origin, region); err != nil {
			return err
		}
		return i.mark(tx, marks, origin, floor, true)
	}); err != nil {
		return nil, fmt.Errorf("synclog: import: %w", err)
	}
	i.noteSigned(origin, signedAt, wrote)
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
func (i *Importer) pullRegion(origin string, after, upto int) ([]Entry, bool, error) {
	latest := make(map[rowKey]Entry)
	cursor := after
	reached := false
	var held int64
	for cursor < upto {
		page, err := i.transport.PullRange(origin, cursor, upto)
		if err != nil {
			return nil, false, err
		}
		advanced := cursor
		for _, e := range page {
			if e.Origin != origin || e.Seq <= after || e.Seq > upto {
				continue
			}
			advanced = max(advanced, e.Seq)
			reached = reached || e.Seq == upto
			k := rowKey{table: e.Table, pk: e.PK}
			if prev, ok := latest[k]; !ok || e.Seq > prev.Seq {
				held += int64(len(e.Body)) - int64(len(prev.Body))
				latest[k] = e
			}
		}
		if len(latest) > orDefault(i.regionRows, maxRegionRows) || held > maxRegionBytes {
			return nil, false, &stallError{origin: origin, cause: fmt.Errorf(
				"the region up to the floor holds more than %d rows or %d body bytes", maxRegionRows, maxRegionBytes)}
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
	return out, reached, nil
}

// checkFloor refuses a floor the log itself contradicts before the mark is
// moved to it. The floor is a meta row other machines can write, and a mark set
// past entries this machine never read would skip them for good: a mark only
// moves forward and the normal path pulls only what follows it.
//
// An honest floor names an entry that is still there -- compaction never drops
// the entry at the floor, since nothing at or below it is later -- and it ends
// a batch, because it was a reader's mark and marks land on batch tails.
func (i *Importer) checkFloor(origin string, floor int, reached bool) error {
	if !reached {
		return badFloor(origin, floor, "names no entry the log holds")
	}
	next, err := i.transport.PullRange(origin, floor, floor+1)
	if err != nil {
		return fmt.Errorf("synclog: import: read past the floor of %s: %w", origin, err)
	}
	for _, e := range next {
		if e.Origin == origin && e.Seq == floor+1 && e.Batch <= floor {
			return badFloor(origin, floor, "falls inside a batch")
		}
	}
	return nil
}

// badFloor is the stall a contradicted floor holds its origin with.
func badFloor(origin string, floor int, why string) error {
	return &stallError{origin: origin, cause: fmt.Errorf("the log's floor %d for this origin %s", floor, why)}
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
// repeat while each applies something. When one applies nothing, or the passes
// run out, the rows still refused are left: left names them, and err is kept
// for a failure that is not a refusal.
func (i *Importer) applyUntilDone(origin string, entries []Entry, total *ImportResult) (left, err error) {
	for pass := 0; len(entries) > 0; pass++ {
		if pass == orDefault(i.passes, maxSnapshotPasses) {
			return fmt.Errorf("the references of %s still refuse them after %d passes", describeRows(entries), pass), nil
		}
		next, applied, err := i.applyPass(origin, entries, total)
		if err != nil {
			return nil, err
		}
		if len(next) > 0 && applied == 0 {
			return fmt.Errorf("the references of %s refuse every remaining row", describeRows(next)), nil
		}
		entries = next
	}
	return nil, nil
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

// orDefault is n, or def when n is zero.
func orDefault(n, def int) int {
	if n == 0 {
		return def
	}
	return n
}

func (i *Importer) checkRegionAfterHold(origin string, mark int, region []Entry, marks map[string]int) (*Hold, error) {
	for _, e := range region {
		if e.After != nil && marks[e.After.Origin] < e.After.Seq {
			names, err := i.labels()
			if err != nil {
				return nil, err
			}
			afterLabel := names[e.After.Origin]
			if afterLabel == "" {
				afterLabel = e.After.Origin
			}
			reason := fmt.Sprintf("%s is waiting for %s at seq %d", origin, afterLabel, e.After.Seq)
			return &Hold{
				Origin: origin,
				Label:  i.labelOf(origin),
				Seq:    mark,
				Reason: reason,
			}, nil
		}
	}
	return nil, nil
}
