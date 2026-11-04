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
// agree. The forwarded-send prefix is the exception: it names a fact two
// serve paths write and ingest and the engine both read, so it lives in the
// Records package both sides can import. The warm/cold words are literals
// on each side, pinned by tests, the convention the prefixes keep.
const (
	queueAcceptPrefix = "queued ("
	queueAdmitPrefix  = "started after "
	forwardWarmSuffix = "(warm)"
	forwardColdSuffix = "(cold)"
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
// between the accept queue entry and the start that ended it, the kind of
// that start, and the last report entry's rusage. Only a reported round
// claims any of them: a report entry is the evidence the round ran to
// completion on this host, so an open or abandoned round keeps all four
// NULL, as do old rounds whose log predates the entries.
func applyBurstStats(r *db.Round, events []store.LogEntry, n int, remote bool, rusage *store.Rusage) {
	wait, kind, haveWait := queueStart(events, n, remote)
	if haveWait {
		r.QueueWaitMS = &wait
	}
	if !remote {
		if kind == "" {
			// No start evidence claims the row: with no send note it stays
			// local -- the reading for pre-entry logs and rounds that never
			// queued -- while a send note whose suffix names no word leaves
			// the kind NULL rather than claiming a start nobody measured.
			if _, _, sent := forwardedStart(events, n); !sent {
				kind = "local"
			}
		}
		if kind != "" {
			k := kind
			r.StartKind = &k
		}
	}
	if rusage != nil {
		cpu, peak := rusage.CPUMS, rusage.PeakMemBytes
		r.RusageCPUMS = &cpu
		r.RusagePeakMemBytes = &peak
	}
}

// queueStart is the round's measured start: the wait from the earliest accept
// entry to the entry that ended the queue, and the kind word that end claims.
// The last start wins, and the wait ends at it: a local admit beats any send
// note for both, first admit among admits (the rule a re-queued round's first
// wait already pinned), and the last send note among sends, because a round
// lost on one node and forwarded to the next really ran at the last send.
// A missing end leaves no wait: a zero is a measured sub-millisecond wait,
// and a round that never queued must not pollute the wait averages with one.
// A remote round claims nothing from a send note -- its row is a mirror of a
// round that ran elsewhere, and its start stamps are the server's -- so only
// an admit can end a remote wait. kind is "" when no admit and no send note
// claim the round at all; the caller reads that as no kind evidence.
func queueStart(events []store.LogEntry, n int, remote bool) (int64, string, bool) {
	queuedTS, haveQueued := earliestAccept(events, n)
	if !haveQueued {
		if _, word, sent := forwardedStart(events, n); sent && !remote {
			return 0, word, false
		}
		return 0, "", false
	}
	if admitTS, ok := firstAdmit(events, n, queuedTS); ok {
		return admitTS.Sub(queuedTS).Milliseconds(), "local", true
	}
	if sendTS, word, sent := forwardedStart(events, n); sent && !remote {
		if sendTS.Before(queuedTS) {
			// A send stamped before the round's accept cannot end its wait;
			// the kind it claims is still the round's own fact.
			return 0, word, false
		}
		return sendTS.Sub(queuedTS).Milliseconds(), word, true
	}
	return 0, "", false
}

// earliestAccept is the timestamp of round n's earliest accept entry.
func earliestAccept(events []store.LogEntry, n int) (time.Time, bool) {
	var queuedTS time.Time
	for _, e := range events {
		if e.Round != n || e.Kind != store.KindQueue || !strings.HasPrefix(e.Note, queueAcceptPrefix) {
			continue
		}
		if queuedTS.IsZero() || e.TS.Before(queuedTS) {
			queuedTS = e.TS
		}
	}
	return queuedTS, !queuedTS.IsZero()
}

// firstAdmit is round n's first admit entry at or after the accept, in log
// order, the same scan the admit-only wait used.
func firstAdmit(events []store.LogEntry, n int, queuedTS time.Time) (time.Time, bool) {
	for _, e := range events {
		if e.Round != n || e.Kind != store.KindQueue || !strings.HasPrefix(e.Note, queueAdmitPrefix) {
			continue
		}
		if e.TS.Before(queuedTS) {
			continue
		}
		return e.TS, true
	}
	return time.Time{}, false
}

// forwardedStart is the last send note of round n -- its timestamp and the
// kind word its suffix carries, "" for a suffix that parses to neither
// (the adopted note, or a note from a writer that words it differently):
// a start with no kind, not a missing start. Log order breaks an exact-time
// tie toward the later entry, so the last send wins.
func forwardedStart(events []store.LogEntry, n int) (time.Time, string, bool) {
	var sendTS time.Time
	var word string
	var sent bool
	for _, e := range events {
		if e.Round != n || e.Kind != store.KindQueue || !strings.HasPrefix(e.Note, store.ForwardedNotePrefix) {
			continue
		}
		if !sent || !e.TS.Before(sendTS) {
			sendTS, word, sent = e.TS, forwardWord(e.Note), true
		}
	}
	return sendTS, word, sent
}

// forwardWord is the warm/cold word a send note's suffix carries, or "" when
// it names neither.
func forwardWord(note string) string {
	switch {
	case strings.HasSuffix(note, forwardWarmSuffix):
		return "warm"
	case strings.HasSuffix(note, forwardColdSuffix):
		return "cold"
	default:
		return ""
	}
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
