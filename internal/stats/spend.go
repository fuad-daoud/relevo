package stats

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

type Spend struct {
	Days     []DayCost // one per local day Since..Until inclusive, oldest first; zero days included
	ThisWeek float64   // [Until-7d, Until)
	LastWeek float64   // [Until-14d, Until-7d)
}

type DayCost struct {
	Day        string // YYYY-MM-DD in Loc
	USD        float64
	Tokens     int64              // summed over every row of the day, like Tokens
	Kinds      TokenCounts        // the day's tokens by kind; Tokens = Kinds.Total()
	ByProvider map[string]float64 // Provider, "(none)" when nil
	// ByCandidate keys "(none)" for a nil candidate, and only rows with a
	// non-zero total.
	ByCandidate      map[string]int64
	TokensByProvider map[string]int64
}

func buildSpend(in Inputs, rows []db.RoundRow, since time.Time, loc *time.Location) Spend {
	sp := Spend{Days: spendDays(since, in.Until, loc)}
	idx := make(map[string]int, len(sp.Days))
	for i, d := range sp.Days {
		idx[d.Day] = i
	}
	kinds := make([]TokenCounts, len(sp.Days))
	for _, r := range rows {
		addSpendDay(&sp, idx, kinds, in, r, loc)
	}
	for i := range sp.Days {
		sp.Days[i].Kinds = kinds[i]
		sp.Days[i].Tokens = kinds[i].Total()
	}
	return sp
}

func spendDays(since, until time.Time, loc *time.Location) []DayCost {
	if until.IsZero() {
		return nil
	}
	var days []DayCost
	end := dayStart(until.In(loc))
	for cur := dayStart(since.In(loc)); !cur.After(end); cur = cur.AddDate(0, 0, 1) {
		days = append(days, DayCost{
			Day:              cur.Format("2006-01-02"),
			ByProvider:       map[string]float64{},
			ByCandidate:      map[string]int64{},
			TokensByProvider: map[string]int64{},
		})
	}
	return days
}

func addSpendDay(sp *Spend, idx map[string]int, kinds []TokenCounts, in Inputs, r db.RoundRow, loc *time.Location) {
	day := r.StartedAt.In(loc).Format("2006-01-02")
	i, ok := idx[day]
	if ok {
		addTokens(&kinds[i], r)
		if n := tokensOf(r).Total(); n > 0 {
			sp.Days[i].ByCandidate[candKey(r)] += n
			sp.Days[i].TokensByProvider[provKey(r)] += n
		}
	}
	if !costKnown(in, r) {
		return
	}
	usd := *r.CostUSD
	if ok {
		sp.Days[i].USD += usd
		sp.Days[i].ByProvider[provKey(r)] += usd
	}
	if in.Until.IsZero() {
		return
	}
	week := in.Until.Add(-7 * 24 * time.Hour)
	fortnight := in.Until.Add(-14 * 24 * time.Hour)
	switch {
	case !r.StartedAt.Before(week) && r.StartedAt.Before(in.Until):
		sp.ThisWeek += usd
	case !r.StartedAt.Before(fortnight) && r.StartedAt.Before(week):
		sp.LastWeek += usd
	}
}

func candKey(r db.RoundRow) string {
	if r.Candidate == nil {
		return "(none)"
	}
	return *r.Candidate
}

func provKey(r db.RoundRow) string {
	if r.Provider == nil {
		return "(none)"
	}
	return *r.Provider
}
