package histq

import (
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// basisUnknown is the CostBasis word a round's cost is not trusted for; histq
// spells the vocabulary itself rather than importing internal/usage.
const basisUnknown = "unknown"

type GroupRow struct {
	Key                                                            string
	Rounds, Reported, Halted, Exited, Switched, DoneNoReport, Open int
	Commits                                                        int
	Tokens                                                         int64
	CostUSD                                                        float64
	Unknown                                                        int
	Last                                                           time.Time
	Rows                                                           []db.RoundRow
}

type Tiles struct {
	Rounds  int
	CostUSD float64
	Unknown int
	Tokens  int64
	Halted  int
	Exited  int
	// MedianDurationMS is the median of the rows whose DurationMS is set, 0
	// when none are; an even count takes the mean of the two middles.
	MedianDurationMS int64
	Bindings         int
	Builders         int
}

// Group buckets rows by an axis and sums each bucket; AxisNone (or "") is no
// regroup and returns nil. day keys are the StartedAt date in loc, so the
// caller's timezone decides where a round's day starts.
func Group(rows []db.RoundRow, by Axis, loc *time.Location) []GroupRow {
	if by == AxisNone || by == "" {
		return nil
	}
	if loc == nil {
		loc = time.Local
	}

	idx := make(map[string]int, len(rows))
	groups := make([]GroupRow, 0, len(rows))
	for _, r := range rows {
		key := groupKey(r, by, loc)
		i, ok := idx[key]
		if !ok {
			i = len(groups)
			idx[key] = i
			groups = append(groups, GroupRow{Key: key})
		}
		g := &groups[i]
		g.Rounds++
		switch r.Outcome {
		case db.OutcomeReported:
			g.Reported++
		case db.OutcomeHalted:
			g.Halted++
		case db.OutcomeExited:
			g.Exited++
		case db.OutcomeSwitched:
			g.Switched++
		case db.OutcomeDoneNoReport:
			g.DoneNoReport++
		case db.OutcomeOpen:
			g.Open++
		}
		if r.Commits != nil {
			g.Commits += *r.Commits
		}
		g.Tokens += rowTokens(r)
		if costKnown(r) {
			g.CostUSD += *r.CostUSD
		}
		if costUnknown(r) {
			g.Unknown++
		}
		if r.StartedAt.After(g.Last) {
			g.Last = r.StartedAt
		}
		g.Rows = append(g.Rows, r)
	}

	for i := range groups {
		sortRowsNewestFirst(groups[i].Rows)
	}

	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if by == AxisDay {
			return a.Key > b.Key
		}
		if a.CostUSD != b.CostUSD {
			return a.CostUSD > b.CostUSD
		}
		if a.Rounds != b.Rounds {
			return a.Rounds > b.Rounds
		}
		return a.Key < b.Key
	})

	return groups
}

// Totals summarises rows for the dashboard's tiles line. It is Totals, not
// Tiles: Go forbids a type and a function of one name.
func Totals(rows []db.RoundRow) Tiles {
	var t Tiles
	bindings := make(map[string]bool, len(rows))
	builders := make(map[string]bool, len(rows))
	durations := make([]int64, 0, len(rows))
	for _, r := range rows {
		t.Rounds++
		t.Tokens += rowTokens(r)
		if costKnown(r) {
			t.CostUSD += *r.CostUSD
		}
		if costUnknown(r) {
			t.Unknown++
		}
		switch r.Outcome {
		case db.OutcomeHalted:
			t.Halted++
		case db.OutcomeExited:
			t.Exited++
		}
		bindings[r.BindingID] = true
		if r.Candidate != nil {
			builders[*r.Candidate] = true
		}
		if r.DurationMS != nil {
			durations = append(durations, *r.DurationMS)
		}
	}
	t.Bindings = len(bindings)
	t.Builders = len(builders)
	t.MedianDurationMS = median(durations)
	return t
}

func groupKey(r db.RoundRow, by Axis, loc *time.Location) string {
	switch by {
	case AxisBinding:
		return r.BindingName
	case AxisRepo:
		return derefKey(r.Repo)
	case AxisFeature:
		return derefKey(r.Feature)
	case AxisTicket:
		return derefKey(r.Ticket)
	case AxisCandidate:
		return derefKey(r.Candidate)
	case AxisAccount:
		return derefKey(r.Account)
	case AxisActor:
		return r.Actor
	case AxisHarness:
		return derefKey(r.Harness)
	case AxisProvider:
		return derefKey(r.Provider)
	case AxisModel:
		return derefKey(r.Model)
	case AxisDay:
		return r.StartedAt.In(loc).Format("2006-01-02")
	case AxisOutcome:
		return r.Outcome
	}
	return ""
}

func derefKey(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

func costKnown(r db.RoundRow) bool {
	return r.CostUSD != nil && (r.CostBasis == nil || *r.CostBasis != basisUnknown)
}

func costUnknown(r db.RoundRow) bool {
	return r.CostUSD == nil || (r.CostBasis != nil && *r.CostBasis == basisUnknown)
}

func sortRowsNewestFirst(rows []db.RoundRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].StartedAt.After(rows[j].StartedAt)
	})
}

func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]int64(nil), xs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
