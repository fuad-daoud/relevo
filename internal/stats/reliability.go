package stats

import (
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
)

type Reliability struct {
	Switches       int     // sum of Switches over rows
	RoundsSwitched int     // rows with Switches > 0
	SwitchPct      float64 // RoundsSwitched / Rounds * 100
	RateLimits     int     // history RateLimited events in [Since, Until)
	SpawnFailures  int     // history SpawnFailed events in [Since, Until)
	// ByHour is the providers with a rate limit in the window, sorted.
	ByHour []HourRow
	Active []availability.Gate
}

type HourRow struct {
	Provider string
	Counts   [24]int
}

func buildReliability(in Inputs, rows []db.RoundRow, loc *time.Location) Reliability {
	rel := Reliability{Active: in.Gates}
	for _, r := range rows {
		rel.Switches += r.Switches
		if r.Switches > 0 {
			rel.RoundsSwitched++
		}
	}
	if len(rows) > 0 {
		rel.SwitchPct = 100 * float64(rel.RoundsSwitched) / float64(len(rows))
	}

	hours := map[string]*[24]int{}
	for _, e := range in.History.Events {
		if e.At.Before(in.Since) {
			continue
		}
		if !in.Until.IsZero() && !e.At.Before(in.Until) {
			continue
		}
		switch e.Kind {
		case availability.RateLimited:
			rel.RateLimits++
			c := hours[e.Provider]
			if c == nil {
				c = &[24]int{}
				hours[e.Provider] = c
			}
			c[e.At.In(loc).Hour()]++
		case availability.SpawnFailed:
			rel.SpawnFailures++
		}
	}

	providers := make([]string, 0, len(hours))
	for p := range hours {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		rel.ByHour = append(rel.ByHour, HourRow{Provider: p, Counts: *hours[p]})
	}
	return rel
}
