package ingest

import (
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Queue note prefixes, repeated from the entries the serve-side writers
// append: the accept census (`internal/serve`) and the admit stamp
// (`internal/relevo`). ingest sits below both in the Layers table, so it
// scans for the spellings instead of importing them, and the copies must
// agree.
const (
	queueAcceptPrefix = "queued ("
	queueAdmitPrefix  = "started after "
)

// roundFacts derives round n's plain facts from its events; the caller sets ID,
// BindingID, Number, Outcome, the builder fields and Switches. remote reports
// whether the round's builder runs on a server: a remote round ran elsewhere,
// so its row claims no start kind while keeping its wait and rusage.
func roundFacts(events []store.LogEntry, n int, remote bool) db.Round {
	var r db.Round
	var earliest, planTS time.Time
	var haveEarliest, havePlanTS bool
	// The round's earlier segments, computed once, which the report entry's own
	// usage folds with.
	prior := store.PriorUsageOf(events, n)
	var haveReport bool
	var lastRusage *store.Rusage

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
			haveReport = true
			lastRusage = e.Rusage
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
	if haveReport {
		applyBurstStats(&r, events, n, remote, lastRusage)
	}
	return r
}

// applyBurstStats fills the burst columns from the round's own log: the wait
// between the accept and admit queue entries, the local start kind, and the
// last report entry's rusage. Only a reported round claims any of them: a
// report entry is the evidence the round ran to completion on this host, so an
// open or abandoned round keeps all four NULL, as do old rounds whose log
// predates the entries.
func applyBurstStats(r *db.Round, events []store.LogEntry, n int, remote bool, rusage *store.Rusage) {
	if wait, ok := queueWaitMS(events, n); ok {
		r.QueueWaitMS = &wait
	}
	if !remote {
		local := "local"
		r.StartKind = &local
	}
	if rusage != nil {
		cpu, peak := rusage.CPUMS, rusage.PeakMemBytes
		r.RusageCPUMS = &cpu
		r.RusagePeakMemBytes = &peak
	}
}

// queueWaitMS is admitTS minus queuedTS in milliseconds for round n: queuedTS
// is the earliest accept entry for the round and admitTS the first admit entry
// at or after it. A missing end leaves no wait: a zero is a measured
// sub-millisecond wait, and a round that never queued must not pollute the
// wait averages with one. A re-queued round measures its first wait only.
func queueWaitMS(events []store.LogEntry, n int) (int64, bool) {
	var queuedTS time.Time
	for _, e := range events {
		if e.Round != n || e.Kind != store.KindQueue || !strings.HasPrefix(e.Note, queueAcceptPrefix) {
			continue
		}
		if queuedTS.IsZero() || e.TS.Before(queuedTS) {
			queuedTS = e.TS
		}
	}
	if queuedTS.IsZero() {
		return 0, false
	}
	for _, e := range events {
		if e.Round != n || e.Kind != store.KindQueue || !strings.HasPrefix(e.Note, queueAdmitPrefix) {
			continue
		}
		if e.TS.Before(queuedTS) {
			continue
		}
		return e.TS.Sub(queuedTS).Milliseconds(), true
	}
	return 0, false
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
