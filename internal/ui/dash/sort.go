package dash

import (
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

// Round rows cycle through roundSortKeys, group rows through groupSortKeys
// (§6, §5 "sort.go -- sort keys per level"). The two lists are disjoint,
// so Model.sortKey alone says which level a key names.
//
// Direction: Model.sortDesc true is descending -- newest, biggest first,
// the sensible default for every column here.
var (
	roundSortKeys = []string{"started", "tokens", "duration", "commits"}
	groupSortKeys = []string{"tokens", "rounds", "halted", "last"}
)

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func isRoundSortKey(s string) bool { return indexOf(roundSortKeys, s) >= 0 }

func isGroupSortKey(s string) bool { return indexOf(groupSortKeys, s) >= 0 }

// roundSortKey is the key the round rows sort by: sortKey when it names a
// round column, "started" (newest first) otherwise.
func (m Model) roundSortKey() string {
	if isRoundSortKey(m.sortKey) {
		return m.sortKey
	}
	return roundSortKeys[0]
}

// groupSortKey is the key the group rows sort by: sortKey when it names a
// group column, "tokens" otherwise.
func (m Model) groupSortKey() string {
	if isGroupSortKey(m.sortKey) {
		return m.sortKey
	}
	return groupSortKeys[0]
}

// sortedRows is rows ordered by the round sort key. It copies, so
// Model.rows keeps the database's own order for the next sort.
func (m Model) sortedRows(rows []db.RoundRow) []db.RoundRow {
	out := append([]db.RoundRow(nil), rows...)
	key, desc := m.roundSortKey(), m.sortDesc
	sort.SliceStable(out, func(i, j int) bool { return roundLess(out[i], out[j], key, desc) })
	return out
}

// sortedGroups is groups ordered by the group sort key. The "-" (none) group
// sorts last whatever the key and direction (§5.7).
func (m Model) sortedGroups(groups []histq.GroupRow) []histq.GroupRow {
	out := append([]histq.GroupRow(nil), groups...)
	key, desc := m.groupSortKey(), m.sortDesc
	sort.SliceStable(out, func(i, j int) bool { return groupLess(out[i], out[j], key, desc) })
	var others, nones []histq.GroupRow
	for _, g := range out {
		if g.Key == "-" || g.Key == "" {
			nones = append(nones, g)
		} else {
			others = append(others, g)
		}
	}
	return append(others, nones...)
}

func cmpInt64(a, b int64, desc bool) bool {
	if desc {
		return a > b
	}
	return a < b
}

func roundLess(a, b db.RoundRow, key string, desc bool) bool {
	switch key {
	case "started":
		if desc {
			return a.StartedAt.After(b.StartedAt)
		}
		return a.StartedAt.Before(b.StartedAt)
	case "tokens":
		return cmpInt64(tokenValue(a), tokenValue(b), desc)
	case "duration":
		return cmpInt64(durationValue(a), durationValue(b), desc)
	case "commits":
		return cmpInt64(commitValue(a), commitValue(b), desc)
	}
	return false
}

func groupLess(a, b histq.GroupRow, key string, desc bool) bool {
	switch key {
	case "tokens":
		return cmpInt64(a.Tokens, b.Tokens, desc)
	case "rounds":
		return cmpInt64(int64(a.Rounds), int64(b.Rounds), desc)
	case "halted":
		return cmpInt64(int64(a.Halted), int64(b.Halted), desc)
	case "last":
		if desc {
			return a.Last.After(b.Last)
		}
		return a.Last.Before(b.Last)
	}
	return false
}

// tokenValue is the sum of a row's four token columns, nil counting zero.
func tokenValue(r db.RoundRow) int64 {
	var n int64
	for _, p := range []*int64{r.InTokens, r.CacheTokens, r.WriteTokens, r.OutTokens} {
		if p != nil {
			n += *p
		}
	}
	return n
}

func durationValue(r db.RoundRow) int64 {
	if r.DurationMS == nil {
		return 0
	}
	return *r.DurationMS
}

func commitValue(r db.RoundRow) int64 {
	if r.Commits == nil {
		return 0
	}
	return int64(*r.Commits)
}
