package stats

import (
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

type ScoreRow struct {
	Token            string
	Rounds, Closed   int
	Reported, Halted int
	DonePct, HaltPct float64 // of Closed; 0 when Closed == 0
	MedianMS         int64   // closed rounds with DurationMS; 0 when none
	HasMedian        bool    // a closed row carried a duration
	TTFTMS           int64
	HasTTFT          bool
	Plan             bool
	CostPerRound     float64 // mean over rows with known cost; valid when HasCost
	HasCost          bool
	CommitsPerRound  float64 // mean over rows with Commits != nil; valid when HasCommits
	HasCommits       bool
	Few              bool // Rounds < 5
	// Bindings is the distinct BindingID count among this candidate's rows.
	Bindings int
	// ReportHalted is the "halted" ReportOutcome count, not the round outcome.
	ReportHalted int
	Switches     int
	// TokenKinds is this candidate's rows, unlike Totals.TokenKinds.
	TokenKinds TokenCounts
}

type scoreAcc struct {
	rounds, closed, reported, halted int
	reportHalted                     int
	switches                         int
	bindings                         map[string]bool
	durations                        []int64
	costSum                          float64
	costN                            int
	commitsSum                       float64
	commitsN                         int
	tokens                           TokenCounts
}

func (a *scoreAcc) add(in Inputs, r db.RoundRow) {
	a.rounds++
	addTokens(&a.tokens, r)
	if a.bindings == nil {
		a.bindings = map[string]bool{}
	}
	a.bindings[r.BindingID] = true
	a.switches += r.Switches
	if r.ReportOutcome != nil && *r.ReportOutcome == "halted" {
		a.reportHalted++
	}
	if closedOutcomes[r.Outcome] {
		a.closed++
		if r.DurationMS != nil {
			a.durations = append(a.durations, *r.DurationMS)
		}
	}
	switch r.Outcome {
	case db.OutcomeReported:
		a.reported++
	case db.OutcomeHalted:
		a.halted++
	}
	if costKnown(in, r) {
		a.costSum += *r.CostUSD
		a.costN++
	}
	if r.Commits != nil {
		a.commitsSum += float64(*r.Commits)
		a.commitsN++
	}
}

func (a *scoreAcc) row(in Inputs, tok string) ScoreRow {
	s := ScoreRow{
		Token:        tok,
		Rounds:       a.rounds,
		Closed:       a.closed,
		Reported:     a.reported,
		Halted:       a.halted,
		MedianMS:     median(a.durations),
		HasMedian:    len(a.durations) > 0,
		Few:          a.rounds < 5,
		Bindings:     len(a.bindings),
		ReportHalted: a.reportHalted,
		Switches:     a.switches,
		TokenKinds:   a.tokens,
	}
	if a.closed > 0 {
		s.DonePct = 100 * float64(a.reported) / float64(a.closed)
		s.HaltPct = 100 * float64(a.halted) / float64(a.closed)
	}
	if in.TTFT != nil {
		if ms, ok := in.TTFT(tok); ok {
			s.TTFTMS, s.HasTTFT = ms, true
		}
	}
	s.Plan = in.IsPlan != nil && in.IsPlan(tok)
	if !s.Plan && a.costN > 0 {
		s.CostPerRound = a.costSum / float64(a.costN)
		s.HasCost = true
	}
	if a.commitsN > 0 {
		s.CommitsPerRound = a.commitsSum / float64(a.commitsN)
		s.HasCommits = true
	}
	return s
}

func buildScorecard(in Inputs, rows []db.RoundRow) []ScoreRow {
	accs := map[string]*scoreAcc{}
	var order []string
	for _, r := range rows {
		if r.Candidate == nil {
			continue
		}
		if in.Keep != nil && !in.Keep(r) {
			continue
		}
		tok := *r.Candidate
		a := accs[tok]
		if a == nil {
			a = &scoreAcc{}
			accs[tok] = a
			order = append(order, tok)
		}
		a.add(in, r)
	}

	out := make([]ScoreRow, 0, len(order))
	for _, tok := range order {
		out = append(out, accs[tok].row(in, tok))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rounds != out[j].Rounds {
			return out[i].Rounds > out[j].Rounds
		}
		return out[i].Token < out[j].Token
	})
	return out
}
