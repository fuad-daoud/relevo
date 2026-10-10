package synclog

// The rule for which of an origin's entries a compaction drops, and when one is
// due. Both are pure so a mistake in either is found by a unit test and not on a
// remote.

import "time"

// compactEvery bounds how long a superseded entry can sit in a quiet log; the
// tenth of growth CompactDue also waits for bounds a busy one.
const compactEvery = 7 * 24 * time.Hour

// CompactDue reports whether an origin should compact its log now.
//
// A log that has never been compacted is due as soon as it holds an entry, and
// an empty log is never due: there is nothing for the run to drop, and a
// recorded size of zero would otherwise make every later tick look like growth.
func CompactDue(lastAt time.Time, lastEntries, entries int, now time.Time) bool {
	if entries <= 0 {
		return false
	}
	if lastAt.IsZero() {
		return true
	}
	if now.Sub(lastAt) >= compactEvery {
		return true
	}
	return entries*10 >= lastEntries*11
}

// compactDrops is the sequences of one origin's entries a compaction to floor
// removes: an entry at or below floor that a later entry for the same row, also
// at or below floor, supersedes.
//
// The later entry has to be at or below floor too. A row whose only newer entry
// sits above the floor keeps its older one, because a reader applying up to the
// floor must meet the parent of every row it applies, and that parent's only
// surviving entry could otherwise be one the reader has not reached.
//
// Delete entries are never dropped for being deletes: a reader that held a row
// and missed its removal still removes it, and a delete is a few bytes.
func compactDrops(entries []Entry, floor int) map[int]bool {
	latest := make(map[rowKey]int)
	for _, e := range entries {
		if e.Seq > floor {
			continue
		}
		k := rowKey{table: e.Table, pk: e.PK}
		if e.Seq > latest[k] {
			latest[k] = e.Seq
		}
	}
	drop := make(map[int]bool)
	for _, e := range entries {
		if e.Seq <= floor && e.Seq < latest[rowKey{table: e.Table, pk: e.PK}] {
			drop[e.Seq] = true
		}
	}
	return drop
}
