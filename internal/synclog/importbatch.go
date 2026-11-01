package synclog

import "sort"

// batchKey is one origin's batch: entries sharing the first entry's sequence
// number. Origin is part of the key because two origins' sequences overlap by
// design.
type batchKey struct {
	origin string
	batch  int
}

// batches divides pulled entries into the whole batches they arrived in, to apply
// in order. Batches group by shared number, not by mark split: a batch starting at
// or before the mark is re-read whole, and half-applying would write an order the
// exporter never produced.
func batches(entries []Entry, marks map[string]int) [][]Entry {
	order := make([]batchKey, 0, len(entries))
	grouped := make(map[batchKey][]Entry, len(entries))
	for _, e := range entries {
		if replayed(marks, e) {
			continue
		}
		key := batchKey{origin: e.Origin, batch: e.Batch}
		if _, seen := grouped[key]; !seen {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], e)
	}
	out := make([][]Entry, 0, len(order))
	for _, key := range order {
		entries := grouped[key]
		// Within a batch the exporter's order applies: deletes before upserts,
		// parents before naming rows, which the foreign keys require.
		sort.SliceStable(entries, func(a, b int) bool { return entries[a].Seq < entries[b].Seq })
		out = append(out, entries)
	}
	return out
}

// replayed reports whether an entry is at or below its origin's mark. Both its
// sequence and its batch decide: the transport hands back whole batches, so
// re-applying one begun before the mark would rewrite rows with content the log
// moved past.
func replayed(marks map[string]int, e Entry) bool {
	mark := marks[e.Origin]
	return e.Seq <= mark || e.Batch <= mark
}

// firstHole is the first sequence in a batch not following its predecessor. A
// batch applies whole only with no hole: advancing past skipped sequences would
// move the mark over entries the transport cannot hand back.
func firstHole(batch []Entry, want int) (int, bool) {
	for i, e := range batch {
		if e.Seq != want+i {
			return e.Seq, true
		}
	}
	return 0, false
}

// tail is the highest sequence a batch carries, the mark covering it. The maximum
// is taken, so an out-of-order transport cannot lower the written mark.
func tail(batch []Entry) int {
	high := 0
	for _, e := range batch {
		if e.Seq > high {
			high = e.Seq
		}
	}
	return high
}
