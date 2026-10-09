// Package stats computes the cockpit's stats report from round rows, the
// availability history and the active gates. It reads no disk, clock or
// environment, so every stats surface shares one pure computation.
package stats

import (
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
)

// Inputs is everything Build reads. A nil func or missing map selects its
// documented zero behaviour, never a panic.
type Inputs struct {
	Rows    []db.RoundRow
	Landed  map[string]bool // binding IDs whose binding reached DONE
	History availability.History
	Gates   []availability.Gate
	// TTFT is a candidate's median first-token time in ms; nil means never set.
	TTFT func(token string) (ms int64, ok bool)
	// IsPlan is nil-safe: nil means no token is a plan.
	IsPlan func(token string) bool
	// Keep filters the scorecard's rows; nil keeps every row.
	Keep func(db.RoundRow) bool
	// Since is the window start; the zero time starts at the oldest row.
	Since time.Time
	// Until is the report's "now": the window's open end.
	Until time.Time
	// Loc is the location days and hours are bucketed in; nil is time.Local.
	Loc *time.Location
}

type Report struct {
	Since, Until time.Time
	Totals       Totals
	Scorecard    []ScoreRow
	// Accounts is the same scorecard grouped by the login a round drew from
	// instead of its candidate. Empty on every host with no accounts, so the
	// report a host without them builds keeps the shape it always had.
	Accounts    []ScoreRow
	Spend       Spend
	Reliability Reliability
	// Repos is keyed by RoundRow.Repo, "(none)" when nil, each with its features
	// and their tickets. Every scratch repo folds into the one "(scratch)" row,
	// which carries the aggregate and its repos in Scratch.
	Repos    []RepoRow
	Outcomes Outcomes
}

type TokenCounts struct {
	In, Cache, Write, Out int64
	Measured              int // rounds with at least one non-nil token field
}

func (c TokenCounts) Total() int64 { return c.In + c.Cache + c.Write + c.Out }

// CachePct is cache over input; ok is false when no input was recorded.
func (c TokenCounts) CachePct() (float64, bool) {
	input := c.In + c.Cache
	if input == 0 {
		return 0, false
	}
	return 100 * float64(c.Cache) / float64(input), true
}

// PerRound is n over the measured rounds; ok is false when none measured.
func (c TokenCounts) PerRound(n int64) (int64, bool) {
	if c.Measured == 0 {
		return 0, false
	}
	return n / int64(c.Measured), true
}

type Totals struct {
	Rounds, Bindings int
	// Candidates is the number of distinct non-nil Candidate tokens.
	Candidates int
	// The Unrecorded bucket never gets a scorecard row.
	Unrecorded  int
	CostUSD     float64 // known basis, non-plan
	PlanRounds  int
	UnknownCost int   // rounds with basis unknown or nil cost (non-plan)
	Tokens      int64 // in+cache+write+out; equals TokenKinds.Total()
	// TokenKinds is Tokens split by kind, unrecorded rows included.
	TokenKinds TokenCounts
	Halted     int
	MedianMS   int64 // closed rounds with DurationMS
}

// closedOutcomes are what the scorecard's DonePct and HaltPct count over.
var closedOutcomes = map[string]bool{
	db.OutcomeReported:     true,
	db.OutcomeHalted:       true,
	db.OutcomeExited:       true,
	db.OutcomeSwitched:     true,
	db.OutcomeDoneNoReport: true,
}

// Build never panics on a nil pointer: every nil RoundRow field is handled.
func Build(in Inputs) Report {
	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	rows := in.Rows

	rep := Report{
		Since: in.Since,
		Until: in.Until,
	}
	if rep.Since.IsZero() {
		rep.Since = oldest(rows)
		if rep.Since.IsZero() {
			rep.Since = in.Until
		}
	}

	rep.Totals = buildTotals(in, rows)
	rep.Scorecard = buildScorecard(in, rows)
	rep.Accounts = buildAccountScorecard(in, rows)
	rep.Spend = buildSpend(in, rows, rep.Since, loc)
	rep.Reliability = buildReliability(in, rows, loc)
	rep.Repos = buildRepos(in, rows)
	rep.Outcomes = buildOutcomes(rows)
	return rep
}

func buildTotals(in Inputs, rows []db.RoundRow) Totals {
	var t Totals
	bindings := map[string]bool{}
	candidates := map[string]bool{}
	var durations []int64
	for _, r := range rows {
		t.Rounds++
		bindings[r.BindingID] = true
		addTokens(&t.TokenKinds, r)
		if r.Outcome == db.OutcomeHalted {
			t.Halted++
		}
		if closedOutcomes[r.Outcome] && r.DurationMS != nil {
			durations = append(durations, *r.DurationMS)
		}
		if r.Candidate == nil {
			t.Unrecorded++
			continue
		}
		candidates[*r.Candidate] = true
		if isPlan(in, r) {
			t.PlanRounds++
			continue
		}
		if costKnown(in, r) {
			t.CostUSD += *r.CostUSD
		} else {
			t.UnknownCost++
		}
	}
	t.Bindings = len(bindings)
	t.Candidates = len(candidates)
	t.MedianMS = median(durations)
	t.Tokens = t.TokenKinds.Total()
	return t
}

// isPlan is false for a nil candidate.
func isPlan(in Inputs, r db.RoundRow) bool {
	return r.Candidate != nil && in.IsPlan != nil && in.IsPlan(*r.Candidate)
}

// costKnown is histq's rule plus the plan exclusion, so the totals agree.
func costKnown(in Inputs, r db.RoundRow) bool {
	return r.CostUSD != nil && (r.CostBasis == nil || *r.CostBasis != "unknown") && !isPlan(in, r)
}

// addTokens counts a row as measured when at least one column is non-nil.
func addTokens(c *TokenCounts, r db.RoundRow) {
	measured := false
	if r.InTokens != nil {
		c.In += *r.InTokens
		measured = true
	}
	if r.CacheTokens != nil {
		c.Cache += *r.CacheTokens
		measured = true
	}
	if r.WriteTokens != nil {
		c.Write += *r.WriteTokens
		measured = true
	}
	if r.OutTokens != nil {
		c.Out += *r.OutTokens
		measured = true
	}
	if measured {
		c.Measured++
	}
}

func tokensOf(r db.RoundRow) TokenCounts {
	var c TokenCounts
	addTokens(&c, r)
	return c
}

// oldest skips an undated row: counting it would pull Since back to year 1 and
// make buildSpend allocate one day per day since then.
func oldest(rows []db.RoundRow) time.Time {
	var t time.Time
	for _, r := range rows {
		if !r.StartedAt.IsZero() && (t.IsZero() || r.StartedAt.Before(t)) {
			t = r.StartedAt
		}
	}
	return t
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// median is histq's, copied so the two stat surfaces agree.
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
