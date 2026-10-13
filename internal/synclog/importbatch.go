package synclog

import "sort"

// batchKey is one batch of one origin: the entries sharing a batch number, which
// is the sequence of the first of them. A batch is keyed by origin too because
// the number belongs to the origin that issued it and two origins' sequences
// overlap by design.
type batchKey struct {
	origin string
	batch  int
}

// batches divides pulled entries into the whole batches they arrived in, in the
// order to apply them. Entries are grouped by the batch they share rather than
// split on the mark, because a batch that begins at or before the mark is
// re-read whole and applying half of it would write an order the exporter never
// produced.
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
		// Within a batch the exporter's own order is the order to apply: it put
		// every parent before the rows naming it and every removal before its
		// parent, and the foreign keys on are what make that order necessary.
		sort.SliceStable(entries, func(a, b int) bool { return entries[a].Seq < entries[b].Seq })
		out = append(out, entries)
	}
	return out
}

// replayed reports whether an entry's sequence is at or below the mark its origin
// has been applied to, and so is not offered to the importer again. Both the
// entry's own sequence and its batch's decide it: the transport hands back whole
// batches, so a batch that began before the mark is a batch whose order this
// machine has already followed, and applying any of it again would rewrite rows
// with content the log has since moved past.
func replayed(marks map[string]int, e Entry) bool {
	mark := marks[e.Origin]
	return e.Seq <= mark || e.Batch <= mark
}

// firstHole is the first sequence in a batch that does not follow the one before
// it, and whether the batch has one. A batch is applicable whole only when its
// entries run want, want+1, ... with nothing missing: a hole at the batch's head
// or between two of its entries would advance the mark over the sequences it
// skipped, and the transport has no way to hand them back. The batch is sorted
// by sequence before this runs.
func firstHole(batch []Entry, want int) (int, bool) {
	for i, e := range batch {
		if e.Seq != want+i {
			return e.Seq, true
		}
	}
	return 0, false
}

// tail is the highest sequence a batch carries, which is the mark that covers it.
// The maximum is taken rather than the last entry read, so a transport handing
// entries back out of order cannot lower the mark this machine writes.
func tail(batch []Entry) int {
	high := 0
	for _, e := range batch {
		if e.Seq > high {
			high = e.Seq
		}
	}
	return high
}
