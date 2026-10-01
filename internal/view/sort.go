package view

import "sort"

// attentionRank orders display states for a human: what needs a decision
// first, what is waiting on the mastermind next, then what is working, then
// what is finished. A halted or stopped chain with a manual round running
// ranks with ACTIVE, so it does not sink below DONE. Unknown states (none
// today) sort after DONE.
var attentionRank = map[string]int{
	"NEEDS YOU": 0,
	"HELD":      1,
	"ACTIVE":    2,
	"HALTED":    2,
	"STOPPED":   2,
	"PAUSED":    3,
	"DONE":      4,
}

func rankOf(display string) int {
	if r, ok := attentionRank[display]; ok {
		return r
	}
	return len(attentionRank)
}

// SortRows orders status rows for a human. OwnerLabel is the primary key
// (ascending, plain <): a client's cards stay contiguous under a header in
// the serve ui. Then attention groups by display state -- NEEDS YOU, HELD,
// ACTIVE, DONE -- and within a group puts the most recent Last.TS first (a
// nil Last last), name as the tiebreak; name is the order Status has
// always returned. With every label empty (a mastermind) the output is
// exactly what the pre-owner implementation returned. Stable; never
// mutates its input.
func SortRows(rows []BindingStatus, attention bool) []BindingStatus {
	out := make([]BindingStatus, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.OwnerLabel != b.OwnerLabel {
			return a.OwnerLabel < b.OwnerLabel
		}
		if attention {
			if ra, rb := rankOf(a.Display), rankOf(b.Display); ra != rb {
				return ra < rb
			}
			// Inside one attention group the rows a human has left the
			// longest come first, so an unacted NEEDS YOU or HELD row does not
			// drift down the list behind fresher ones.
			if sa, sb := a.Stale != "", b.Stale != ""; sa != sb {
				return sa
			}
			switch {
			case a.Last != nil && b.Last != nil && !a.Last.TS.Equal(b.Last.TS):
				return a.Last.TS.After(b.Last.TS)
			case a.Last != nil && b.Last == nil:
				return true
			case a.Last == nil && b.Last != nil:
				return false
			}
		}
		return a.Name < b.Name
	})
	return out
}
