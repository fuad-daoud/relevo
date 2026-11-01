package ingest

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// roundFacts derives round n's plain facts from its events; the caller sets ID,
// BindingID, Number, Outcome, the builder fields and Switches.
func roundFacts(events []store.LogEntry, n int) db.Round {
	var r db.Round
	var earliest, planTS time.Time
	var haveEarliest, havePlanTS bool
	// The round's earlier segments, computed once, which the report entry's own
	// usage folds with.
	prior := store.PriorUsageOf(events, n)

	for _, e := range events {
		if e.Round != n {
			continue
		}
		if !haveEarliest || e.TS.Before(earliest) {
			earliest, haveEarliest = e.TS, true
		}
		if store.IsPromptKind(e.Kind) {
			if !havePlanTS || e.TS.Before(planTS) {
				planTS, havePlanTS = e.TS, true
			}
			applyPlanTier(&r, e)
			continue
		}
		switch e.Kind {
		case store.KindDiff:
			applyDiff(&r, e)
		case store.KindReport:
			applyReport(&r, e, prior)
		case store.KindExit, store.KindSwitch, store.KindStop:
			if r.ClosedAt == nil {
				ts := e.TS
				r.ClosedAt = &ts
			}
		}
	}

	if havePlanTS {
		r.StartedAt = planTS
	} else if haveEarliest {
		r.StartedAt = earliest
	}
	return r
}

func applyPlanTier(r *db.Round, e store.LogEntry) {
	if e.Tier == "" {
		return
	}
	tier := e.Tier
	r.Tier = &tier
}

func applyDiff(r *db.Round, e store.LogEntry) {
	if e.Tree == "" {
		return
	}
	commits, tree := e.Commits, e.Tree
	r.Commits = &commits
	r.Tree = &tree
}

func applyReport(r *db.Round, e store.LogEntry, prior usage.Usage) {
	ts := e.TS
	r.ClosedAt = &ts
	applyGate(r, e.Gate)
	applyUsage(r, totalUsage(prior, e.Usage))
	outcome := e.Outcome
	if outcome == "" {
		outcome = "unstructured"
	}
	r.ReportOutcome = &outcome
}

// totalUsage folds a round's earlier segments into the report entry's own usage,
// so the row counts every session the round ran. A report entry with no usage
// keeps the row figureless, never a partial total in place of an absent segment.
func totalUsage(prior usage.Usage, u *usage.Usage) *usage.Usage {
	if u == nil {
		return nil
	}
	total := usage.Add(*u, prior)
	return &total
}

func applyGate(r *db.Round, g *store.GateRecord) {
	if g == nil {
		return
	}
	result, exitCode, durationMS := g.Result, g.ExitCode, g.DurationMS
	r.GateResult = &result
	r.GateExit = &exitCode
	r.GateDurationMS = &durationMS
}

func applyUsage(r *db.Round, u *usage.Usage) {
	if u == nil {
		return
	}
	in, cache, write, out := u.Tokens.In, u.Tokens.CacheRead, u.Tokens.CacheWrite, u.Tokens.Out
	cost, basis := u.Cost.USD, string(u.Cost.Basis)
	r.InTokens = &in
	r.CacheTokens = &cache
	r.WriteTokens = &write
	r.OutTokens = &out
	r.CostUSD = &cost
	r.CostBasis = &basis
}
