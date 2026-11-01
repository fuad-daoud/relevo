package view

import (
	"slices"
	"strings"
)

// attentionRankUnknown sorts after every named group, so an unrecognised
// state sinks below DONE rather than floating to the top.
const attentionRankUnknown = 4

// rankOf orders display states for a human. A halted or stopped chain with a
// manual round running ranks with ACTIVE, so it does not sink below DONE. A
// switch, not a table: this runs once per comparison of the fleet's hot sort.
func rankOf(display string) int {
	switch display {
	case "NEEDS YOU":
		return 0
	case "ACTIVE", "HALTED", "STOPPED":
		return 1
	case "PAUSED":
		return 2
	case "DONE":
		return 3
	default:
		return attentionRankUnknown
	}
}

// compareRows is SortRows' ordering on two rows: OwnerLabel is the primary key
// so a client's cards stay contiguous under a header in the serve ui. Then
// attention groups by display state, most recent first (a nil Last last), name
// as the tiebreak. Zero means "neither first": the rows compare equal, and a
// stable sort keeps their input order.
func compareRows(a, b BindingStatus, attention bool) int {
	if a.OwnerLabel != b.OwnerLabel {
		return strings.Compare(a.OwnerLabel, b.OwnerLabel)
	}
	if attention {
		if ra, rb := rankOf(a.Display), rankOf(b.Display); ra != rb {
			return compareInts(ra, rb)
		}
		// Inside one attention group the rows a human has left the longest come
		// first, so an unacted NEEDS YOU row does not drift down the list behind
		// fresher ones.
		if sa, sb := a.Stale != "", b.Stale != ""; sa != sb {
			return compareBools(sa, sb)
		}
		switch {
		case a.Last != nil && b.Last != nil && !a.Last.TS.Equal(b.Last.TS):
			if a.Last.TS.After(b.Last.TS) {
				return -1
			}
			return 1
		case a.Last != nil && b.Last == nil:
			return -1
		case a.Last == nil && b.Last != nil:
			return 1
		}
	}
	return strings.Compare(a.Name, b.Name)
}

func compareInts(a, b int) int {
	if a < b {
		return -1
	}
	return 1
}

func compareBools(a, b bool) int {
	if a == b {
		return 0
	}
	if a {
		return -1
	}
	return 1
}

// SortRows orders status rows for a human; name is the order Status has always
// returned. It sorts a permutation of row indices and copies once at the end,
// so the elements it moves are machine words rather than whole rows. Stable,
// so an input order never shows through; never mutates its input.
func SortRows(rows []BindingStatus, attention bool) []BindingStatus {
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(i, j int) int {
		return compareRows(rows[i], rows[j], attention)
	})
	out := make([]BindingStatus, len(rows))
	for i, src := range order {
		out[i] = rows[src]
	}
	return out
}
