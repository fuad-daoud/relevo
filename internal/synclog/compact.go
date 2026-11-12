package synclog

// The rule for which of an origin's entries a compaction drops, as a pure
// function a unit test finds rather than a remote.

// compactDrops is the sequences a compaction to floor removes: an entry at or
// below floor superseded by a later entry for the same row, also at or below
// floor. The later entry must sit at or below floor too, or a reader to the floor
// would miss the parent of a row it applies. A delete is never dropped for being
// a delete: a reader that missed a removal still removes it, and a delete is a
// few bytes.
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
