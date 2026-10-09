package stats

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
)

// stNow is the fixture's "now": every window below is relative to it.
var stNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func stStr(v string) *string   { return &v }
func stInt(v int) *int         { return &v }
func stI64(v int64) *int64     { return &v }
func stF64(v float64) *float64 { return &v }

func stAt(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func byToken(rep Report) map[string]ScoreRow {
	by := map[string]ScoreRow{}
	for _, s := range rep.Scorecard {
		by[s.Token] = s
	}
	return by
}

// TestScorecard pins each scorecard field group against its own fixture.
func TestScorecard(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    Inputs
		check func(t *testing.T, rep Report)
	}{
		{
			name: "rates and medians over closed rounds",
			in: Inputs{Until: stNow, Loc: time.UTC, Rows: []db.RoundRow{
				{Candidate: stStr("a"), Outcome: db.OutcomeReported, DurationMS: stI64(600_000)},
				{Candidate: stStr("a"), Outcome: db.OutcomeOpen},
				{Candidate: stStr("a"), Outcome: db.OutcomeHalted, DurationMS: stI64(1_200_000)},
				{Candidate: stStr("b"), Outcome: db.OutcomeReported, DurationMS: stI64(100_000)},
				{Candidate: stStr("b"), Outcome: db.OutcomeReported, DurationMS: stI64(300_000)},
				{Candidate: stStr("b"), Outcome: db.OutcomeReported, DurationMS: stI64(500_000)},
			}},
			check: checkScorecardRates,
		},
		{
			name: "unrecorded rows and Keep",
			in: Inputs{Until: stNow, Loc: time.UTC, Rows: []db.RoundRow{
				{Candidate: stStr("a"), Outcome: db.OutcomeReported},
				{Candidate: nil, Outcome: db.OutcomeReported},
				{Candidate: stStr("b"), Outcome: db.OutcomeReported},
			}, Keep: func(r db.RoundRow) bool {
				return r.Candidate != nil && *r.Candidate != "b"
			}},
			check: checkScorecardUnrecorded,
		},
		{
			name: "plan rows report no cost",
			in: Inputs{Until: stNow, Loc: time.UTC, IsPlan: func(tok string) bool { return tok == "plan/x" }, Rows: []db.RoundRow{
				{Candidate: stStr("plan/x"), Outcome: db.OutcomeReported, CostUSD: stF64(9), CostBasis: stStr("measured")},
				{Candidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(1), CostBasis: stStr("measured")},
				{Candidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(3), CostBasis: stStr("measured")},
				{Candidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(2), CostBasis: stStr("unknown")},
				// A cost with no basis at all: costKnown counts it.
				{Candidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(5)},
				{Candidate: stStr("paid/y"), Outcome: db.OutcomeReported},
				{Candidate: stStr("paid/y"), Outcome: db.OutcomeReported},
			}},
			check: checkScorecardPlanCost,
		},
		{
			name: "bindings, report halts and switches",
			in: Inputs{Until: stNow, Loc: time.UTC, Rows: []db.RoundRow{
				{BindingID: "b1", Candidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 0},
				{BindingID: "b2", Candidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 2, ReportOutcome: stStr("halted")},
				{BindingID: "b2", Candidate: stStr("a"), Outcome: db.OutcomeHalted, Switches: 1, ReportOutcome: stStr("done")},
				{BindingID: "b3", Candidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 0},
			}},
			check: checkScorecardBindings,
		},
		{
			name: "per-candidate token kinds leave the unrecorded bucket out",
			in: Inputs{Until: stNow, Loc: time.UTC, Rows: []db.RoundRow{
				{Candidate: stStr("a"), InTokens: stI64(100), CacheTokens: stI64(900), OutTokens: stI64(10)},
				{Candidate: stStr("a"), OutTokens: stI64(90)},
				{Candidate: stStr("b"), InTokens: stI64(7)},
				{Candidate: nil, InTokens: stI64(1000)},
			}},
			check: checkScorecardTokens,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.check(t, Build(c.in))
		})
	}
}

func checkScorecardRates(t *testing.T, rep Report) {
	by := byToken(rep)
	a := by["a"]
	if a.Closed != 2 || a.Reported != 1 || a.Halted != 1 {
		t.Fatalf("a: closed/reported/halted = %d/%d/%d, want 2/1/1", a.Closed, a.Reported, a.Halted)
	}
	if a.DonePct != 50 || a.HaltPct != 50 {
		t.Errorf("a: done/halt = %v/%v, want 50/50", a.DonePct, a.HaltPct)
	}
	if a.MedianMS != 900_000 {
		t.Errorf("a: median = %d, want 900000 (mean of the two middles)", a.MedianMS)
	}
	b := by["b"]
	if b.Closed != 3 || b.DonePct != 100 {
		t.Fatalf("b: closed/done = %d/%v, want 3/100", b.Closed, b.DonePct)
	}
	if b.MedianMS != 300_000 {
		t.Errorf("b: median = %d, want 300000 (the odd middle)", b.MedianMS)
	}
}

func checkScorecardUnrecorded(t *testing.T, rep Report) {
	if rep.Totals.Unrecorded != 1 {
		t.Errorf("Totals.Unrecorded = %d, want 1: the nil candidate's rounds", rep.Totals.Unrecorded)
	}
	if rep.Totals.Candidates != 2 {
		t.Errorf("Totals.Candidates = %d, want 2: the distinct non-nil tokens", rep.Totals.Candidates)
	}
	if rep.Totals.Rounds != 3 {
		t.Errorf("Totals.Rounds = %d, want 3: Totals counts every row", rep.Totals.Rounds)
	}
	if len(rep.Scorecard) != 1 || rep.Scorecard[0].Token != "a" {
		t.Fatalf("scorecard = %+v, want only a", rep.Scorecard)
	}
}

func checkScorecardPlanCost(t *testing.T, rep Report) {
	by := byToken(rep)
	p := by["plan/x"]
	if !p.Plan || p.HasCost {
		t.Errorf("plan row = plan %v / hasCost %v, want true/false", p.Plan, p.HasCost)
	}
	if !p.Few {
		t.Errorf("plan row Few = false, want true (1 round)")
	}
	y := by["paid/y"]
	if !y.HasCost {
		t.Fatalf("paid row HasCost = false, want true")
	}
	if y.CostPerRound != 3 {
		t.Errorf("paid row cost = %v, want 3 (the measured rows plus the nil-basis row)", y.CostPerRound)
	}
	if y.Few {
		t.Errorf("paid row Few = true, want false (6 rounds)")
	}
}

func checkScorecardBindings(t *testing.T, rep Report) {
	if len(rep.Scorecard) != 1 {
		t.Fatalf("scorecard = %+v, want one row", rep.Scorecard)
	}
	s := rep.Scorecard[0]
	if s.Bindings != 3 {
		t.Errorf("Bindings = %d, want 3 (b1, b2, b3)", s.Bindings)
	}
	if s.ReportHalted != 1 {
		t.Errorf("ReportHalted = %d, want 1 (the harnessed halted report)", s.ReportHalted)
	}
	if s.Switches != 3 {
		t.Errorf("Switches = %d, want 3 (0+2+1+0)", s.Switches)
	}
}

func checkScorecardTokens(t *testing.T, rep Report) {
	by := byToken(rep)
	a := by["a"].TokenKinds
	if a.In != 100 || a.Cache != 900 || a.Out != 100 || a.Measured != 2 || a.Total() != 1100 {
		t.Errorf("a TokenKinds = %+v, want In 100, Cache 900, Out 100, Measured 2, Total 1100", a)
	}
	b := by["b"].TokenKinds
	if b.In != 7 || b.Measured != 1 || b.Total() != 7 {
		t.Errorf("b TokenKinds = %+v, want In 7, Measured 1, Total 7", b)
	}
	if rep.Totals.TokenKinds.In != 1107 {
		t.Errorf("Totals In = %d, want 1107 (the unrecorded row's 1000 included)", rep.Totals.TokenKinds.In)
	}
}

// TestSpend pins the day series, its splits and the two week windows.
func TestSpend(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    Inputs
		check func(t *testing.T, rep Report)
	}{
		{
			name: "days and week windows",
			in: Inputs{Since: stAt(2026, time.September, 22, 0, 0), Until: stNow, Loc: time.UTC, Rows: []db.RoundRow{
				// Exactly Until-7d: this week.
				{StartedAt: stNow.Add(-7 * 24 * time.Hour), CostUSD: stF64(1), CostBasis: stStr("measured")},
				// One millisecond earlier: last week.
				{StartedAt: stNow.Add(-7*24*time.Hour - time.Millisecond), CostUSD: stF64(2), CostBasis: stStr("measured")},
				// Exactly Until-14d: last week.
				{StartedAt: stNow.Add(-14 * 24 * time.Hour), CostUSD: stF64(4), CostBasis: stStr("measured")},
				// One millisecond earlier: neither week.
				{StartedAt: stNow.Add(-14*24*time.Hour - time.Millisecond), CostUSD: stF64(8), CostBasis: stStr("measured")},
				// Inside the day window, with a provider.
				{StartedAt: stAt(2026, time.September, 23, 5, 0), Provider: stStr("p1"), CostUSD: stF64(16), CostBasis: stStr("measured")},
				// Inside the day window, with no provider.
				{StartedAt: stAt(2026, time.September, 24, 6, 0), CostUSD: stF64(32), CostBasis: stStr("measured")},
			}},
			check: checkSpendDaysWeeks,
		},
		{
			name: "day token breakdowns",
			in: Inputs{Since: stAt(2026, time.September, 23, 0, 0), Until: stAt(2026, time.September, 24, 12, 0), Loc: time.UTC, Rows: []db.RoundRow{
				{StartedAt: stAt(2026, time.September, 23, 9, 0), Candidate: stStr("A"), Provider: stStr("p1"),
					InTokens: stI64(10), CacheTokens: stI64(100), OutTokens: stI64(5)},
				{StartedAt: stAt(2026, time.September, 23, 10, 0), Candidate: stStr("B"), Provider: stStr("p2"),
					OutTokens: stI64(7)},
				{StartedAt: stAt(2026, time.September, 24, 9, 0), Candidate: stStr("A"), InTokens: stI64(1)},
			}},
			check: checkSpendBreakdowns,
		},
		{
			name: "tokens bucket by local day",
			in: Inputs{Since: stAt(2026, time.September, 22, 0, 0), Until: stAt(2026, time.September, 25, 12, 0),
				Loc: time.FixedZone("X", 2*3600), Rows: []db.RoundRow{
					// 23:30 UTC on the 23rd is 01:30 local on the 24th.
					{StartedAt: stAt(2026, time.September, 23, 23, 30), InTokens: stI64(100)},
					// 22:30 UTC on the 24th is 00:30 local on the 25th.
					{StartedAt: stAt(2026, time.September, 24, 22, 30), InTokens: stI64(7)},
					// 21:30 UTC on the 23rd is still the 23rd locally.
					{StartedAt: stAt(2026, time.September, 23, 21, 30), InTokens: stI64(3)},
				}},
			check: checkSpendLocalDays,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.check(t, Build(c.in))
		})
	}
}

func checkSpendDaysWeeks(t *testing.T, rep Report) {
	if rep.Spend.ThisWeek != 49 {
		t.Errorf("ThisWeek = %v, want 49 (1 on the boundary plus the two in-window days)", rep.Spend.ThisWeek)
	}
	if rep.Spend.LastWeek != 6 {
		t.Errorf("LastWeek = %v, want 6", rep.Spend.LastWeek)
	}
	if len(rep.Spend.Days) != 3 {
		t.Fatalf("days = %d, want 3 (22nd, 23rd, 24th)", len(rep.Spend.Days))
	}
	wantDays := []string{"2026-09-22", "2026-09-23", "2026-09-24"}
	for i, want := range wantDays {
		if rep.Spend.Days[i].Day != want {
			t.Errorf("day %d = %q, want %q", i, rep.Spend.Days[i].Day, want)
		}
	}
	if rep.Spend.Days[0].USD != 0 || len(rep.Spend.Days[0].ByProvider) != 0 {
		t.Errorf("zero day = %+v, want 0 and no providers", rep.Spend.Days[0])
	}
	for _, d := range rep.Spend.Days {
		var sum float64
		for _, v := range d.ByProvider {
			sum += v
		}
		if sum != d.USD {
			t.Errorf("day %s: provider split %v != USD %v", d.Day, sum, d.USD)
		}
	}
	if got := rep.Spend.Days[1].ByProvider["p1"]; got != 16 {
		t.Errorf("23rd p1 = %v, want 16", got)
	}
	if got := rep.Spend.Days[2].ByProvider["(none)"]; got != 32 {
		t.Errorf("24th (none) = %v, want 32", got)
	}
}

func checkSpendBreakdowns(t *testing.T, rep Report) {
	if len(rep.Spend.Days) != 2 {
		t.Fatalf("days = %d, want 2", len(rep.Spend.Days))
	}
	d1, d2 := rep.Spend.Days[0], rep.Spend.Days[1]
	if d1.Kinds.In != 10 || d1.Kinds.Cache != 100 || d1.Kinds.Out != 12 {
		t.Errorf("day 1 Kinds = %+v, want In 10, Cache 100, Out 12", d1.Kinds)
	}
	if got := d1.ByCandidate; got["A"] != 115 || got["B"] != 7 || len(got) != 2 {
		t.Errorf("day 1 ByCandidate = %v, want A 115, B 7", got)
	}
	if got := d1.TokensByProvider; got["p1"] != 115 || got["p2"] != 7 || len(got) != 2 {
		t.Errorf("day 1 TokensByProvider = %v, want p1 115, p2 7", got)
	}
	if got := d2.ByCandidate; got["A"] != 1 || len(got) != 1 {
		t.Errorf("day 2 ByCandidate = %v, want A 1", got)
	}
	for i, d := range rep.Spend.Days {
		if d.Tokens != d.Kinds.Total() {
			t.Errorf("day %d Tokens = %d, want Kinds.Total() = %d", i, d.Tokens, d.Kinds.Total())
		}
	}
}

func checkSpendLocalDays(t *testing.T, rep Report) {
	want := map[string]int64{"2026-09-23": 3, "2026-09-24": 100, "2026-09-25": 7}
	for _, d := range rep.Spend.Days {
		if got, ok := want[d.Day]; ok && d.Tokens != got {
			t.Errorf("%s Tokens = %d, want %d", d.Day, d.Tokens, got)
		}
	}
	if got := rep.Spend.Days[0].Tokens; got != 0 {
		t.Errorf("the zero day's Tokens = %d, want 0", got)
	}
}

// TestGroupRows pins the repo buckets, their per-repo feature and ticket
// children, and the per-repo feature-less and ticket-less buckets .
func TestGroupRows(t *testing.T) {
	t.Parallel()
	for _, c := range groupRowCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.check(t, Build(Inputs{Rows: c.rows, Landed: c.landed, Until: stNow, Loc: time.UTC}))
		})
	}
}

// groupRowCase is one TestGroupRows case.
type groupRowCase struct {
	name   string
	rows   []db.RoundRow
	landed map[string]bool
	check  func(t *testing.T, rep Report)
}

// groupRowCases is TestGroupRows' table: the repo-scoping cases and the
// breakdown cases a repo row carries.
func groupRowCases() []groupRowCase {
	return append(repoScopedRowCases(), breakdownRowCases()...)
}

// repoScopedRowCases pins the repo buckets, their per-repo children and the
// per-repo (no feature) bucket with the tickets nested under each feature.
func repoScopedRowCases() []groupRowCase {
	return append([]groupRowCase{
		{
			name: "rounds per land and an unlanded group",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Outcome: db.OutcomeReported},
				{BindingID: "b2", Repo: stStr("A"), Feature: stStr("f1"), Outcome: db.OutcomeReported},
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Outcome: db.OutcomeReported},
				{BindingID: "b3", Repo: stStr("A"), Outcome: db.OutcomeHalted},
				{BindingID: "b9", Repo: stStr("B"), Feature: stStr("f2"), Outcome: db.OutcomeHalted},
			},
			landed: map[string]bool{"b1": true, "b2": true},
			check:  checkGroupLanded,
		},
		{
			name: "one label under two repos",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("shared"), InTokens: stI64(10)},
				{BindingID: "b2", Repo: stStr("A"), Feature: stStr("shared"), InTokens: stI64(5)},
				{BindingID: "b3", Repo: stStr("B"), Feature: stStr("shared"), InTokens: stI64(100)},
			},
			check: checkGroupLabelAcrossRepos,
		},
		{
			name: "per-repo (no feature) numbers and the feature's tickets",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Ticket: stStr("t1")},
				{BindingID: "b2", Repo: stStr("A")},
				{BindingID: "b3", Repo: stStr("A"), Feature: stStr("f1")},
				{BindingID: "b4", Repo: stStr("B")},
			},
			check: checkGroupRepoNoneBuckets,
		},
		{
			name: "a repo with no labels expands to nothing",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1")},
				{BindingID: "b2", Repo: stStr("B")},
				{BindingID: "b3", Repo: stStr("B")},
			},
			check: checkGroupUnlabelledRepo,
		},
	}, append(ticketScopedRowCases(), scratchRowCases()...)...)
}

// scratchRowCases pins the fold: the scratch repos (absolute-path keys) gather
// under one ScratchKey row, a remote repo keeps its own row beside it, a round
// with no repo stays out of the fold, and each child keeps its own feature
// buckets while the fold's stay zero.
func scratchRowCases() []groupRowCase {
	one := stStr("/tmp/one")
	two := stStr("/tmp/two")
	return []groupRowCase{
		{
			name: "scratch repos fold into one row",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: one, Feature: stStr("f1"), InTokens: stI64(10)},
				{BindingID: "b2", Repo: two, InTokens: stI64(20)},
			},
			check: checkScratchFoldRow,
		},
		{
			name: "scratch fold totals equal the sum of its repos",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: one, Feature: stStr("f1"), ReportOutcome: stStr("done"),
					Commits: stInt(2), Outcome: db.OutcomeReported, InTokens: stI64(10)},
				// The same binding in two scratch repos: Bindings counts it once.
				{BindingID: "b1", Repo: two, ReportOutcome: stStr("halted"),
					Commits: stInt(1), Outcome: db.OutcomeHalted, InTokens: stI64(20)},
			},
			landed: map[string]bool{"b1": true},
			check:  checkScratchFoldTotals,
		},
		{
			name: "a remote repo keeps its own row beside the fold",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: one, InTokens: stI64(10)},
				{BindingID: "b2", Repo: stStr("https://github.com/o/r"), Feature: stStr("f1"),
					Ticket: stStr("t1"), InTokens: stI64(100)},
				{BindingID: "b3"},
			},
			check: checkScratchRemoteUnchanged,
		},
		{
			name: "no scratch repo builds no fold row",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("https://github.com/o/r")},
				{BindingID: "b2"},
			},
			check: checkScratchNoFold,
		},
		{
			name: "a round with no repo is not scratch",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: one, InTokens: stI64(10)},
				{BindingID: "b2", InTokens: stI64(5)},
			},
			check: checkScratchNoneNotFolded,
		},
		{
			name: "scratch children keep their own feature buckets",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: one, Feature: stStr("f1"), InTokens: stI64(10)},
				{BindingID: "b2", Repo: one, InTokens: stI64(5)},
				{BindingID: "b3", Repo: two, Feature: stStr("f1"), InTokens: stI64(100)},
			},
			check: checkScratchChildFeatures,
		},
	}
}

// ticketScopedRowCases pins the labelled tickets nested under a repo's features
// and under its (no feature) bucket, each copy counting only its own feature's
// rows in that repo.
func ticketScopedRowCases() []groupRowCase {
	return []groupRowCase{
		{
			name: "a feature with tickets keeps its ticketless rounds",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Ticket: stStr("t1"), InTokens: stI64(10)},
				{BindingID: "b2", Repo: stStr("A"), Feature: stStr("f1"), InTokens: stI64(5)},
			},
			check: checkGroupFeatureTickets,
		},
		{
			name: "a ticket under (no feature)",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Ticket: stStr("t9"), InTokens: stI64(7)},
			},
			check: checkGroupNoFeatureTicket,
		},
		{
			name: "one ticket under two features",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Ticket: stStr("t1"), InTokens: stI64(10)},
				{BindingID: "b2", Repo: stStr("A"), Feature: stStr("f2"), Ticket: stStr("t1"), InTokens: stI64(100)},
			},
			check: checkGroupTicketAcrossFeatures,
		},
		{
			name: "one ticket label under two repos",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Ticket: stStr("t1"), InTokens: stI64(10)},
				{BindingID: "b2", Repo: stStr("B"), Feature: stStr("f2"), Ticket: stStr("t1"), InTokens: stI64(100)},
			},
			check: checkGroupTicketAcrossRepos,
		},
		{
			name: "a repo with no labels keeps every round in (no feature)",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("B")},
				{BindingID: "b2", Repo: stStr("B")},
			},
			check: checkGroupNoLabelsRepo,
		},
	}
}

// breakdownRowCases pins the per-group breakdowns a repo row carries.
func breakdownRowCases() []groupRowCase {
	return []groupRowCase{
		{
			name: "bindings, reports, commits and candidates",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), ReportOutcome: stStr("done"), Commits: stInt(2), Candidate: stStr("A")},
				{BindingID: "b2", Repo: stStr("A"), ReportOutcome: stStr("done"), Candidate: stStr("A")},
				{BindingID: "b3", Repo: stStr("A"), ReportOutcome: stStr("halted"), Commits: stInt(1), Candidate: stStr("B")},
				{BindingID: "b1", Repo: stStr("A"), Commits: stInt(0)},
			},
			check: checkGroupBreakdowns,
		},
		{
			name: "tokens per repo and child",
			rows: []db.RoundRow{
				{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), InTokens: stI64(10), OutTokens: stI64(5)},
				{BindingID: "b2", Repo: stStr("A"), Feature: stStr("f1"), CacheTokens: stI64(100)},
				{BindingID: "b3", Repo: stStr("B"), Feature: stStr("f2"), WriteTokens: stI64(2)},
				{BindingID: "b4"},
			},
			check: checkGroupTokens,
		},
		{
			name:  "no rows builds no repos",
			check: checkGroupNoRows,
		},
	}
}

func checkGroupLanded(t *testing.T, rep Report) {
	if len(rep.Repos) != 2 {
		t.Fatalf("repos = %+v, want A and B", rep.Repos)
	}
	a := rep.Repos[0]
	if a.Key != "A" || a.Rounds != 4 || a.Halted != 1 || a.Landed != 2 {
		t.Fatalf("repo A = %+v, want 4 rounds, 1 halted, 2 landed", a)
	}
	if a.RoundsPerLand != 1.5 {
		t.Errorf("repo A rounds/land = %v, want 1.5 (3 rounds over 2 landed)", a.RoundsPerLand)
	}
	if len(a.Features) != 1 || a.Features[0].Key != "f1" || a.Features[0].Rounds != 3 ||
		a.Features[0].Landed != 2 || a.Features[0].RoundsPerLand != 1.5 {
		t.Errorf("repo A features = %+v, want f1 with 3 rounds, 2 landed, 1.5 rounds/land", a.Features)
	}
	if a.NoFeature.Key != "(none)" || a.NoFeature.Rounds != 1 {
		t.Errorf("repo A NoFeature = %+v, want the one unlabelled halt", a.NoFeature)
	}
	b := rep.Repos[1]
	if b.Landed != 0 || b.RoundsPerLand != 0 {
		t.Errorf("repo B = %+v, want 0 landed and 0 rounds/land", b)
	}
	if len(b.Features) != 1 || b.Features[0].Key != "f2" {
		t.Errorf("repo B features = %+v, want f2", b.Features)
	}
}

// checkGroupLabelAcrossRepos pins the repo scoping: one label two repos use
// appears under each with only that repo's rows counted.
func checkGroupLabelAcrossRepos(t *testing.T, rep Report) {
	byKey := map[string]RepoRow{}
	for _, r := range rep.Repos {
		byKey[r.Key] = r
	}
	a := byKey["A"]
	if len(a.Features) != 1 || a.Features[0].Key != "shared" {
		t.Fatalf("repo A features = %+v, want one shared label", a.Features)
	}
	if a.Features[0].Rounds != 2 || a.Features[0].Tokens != 15 {
		t.Errorf("A/shared = %+v, want 2 rounds and 15 tokens (its own rows only)", a.Features[0])
	}
	b := byKey["B"]
	if len(b.Features) != 1 || b.Features[0].Key != "shared" {
		t.Fatalf("repo B features = %+v, want one shared label", b.Features)
	}
	if b.Features[0].Rounds != 1 || b.Features[0].Tokens != 100 {
		t.Errorf("B/shared = %+v, want 1 round and 100 tokens", b.Features[0])
	}
}

// checkGroupRepoNoneBuckets pins the per-repo (no feature) numbers and the
// tickets nested under the repo's labelled feature: a feature's numbers include
// its ticketless rounds, and its ticket list holds only the labelled rounds.
func checkGroupRepoNoneBuckets(t *testing.T, rep Report) {
	byKey := map[string]RepoRow{}
	for _, r := range rep.Repos {
		byKey[r.Key] = r
	}
	a := byKey["A"]
	if a.NoFeature.Key != "(none)" || a.NoFeature.Rounds != 1 {
		t.Errorf("A NoFeature = %+v, want 1 round keyed (none)", a.NoFeature)
	}
	if len(a.NoFeature.Tickets) != 0 {
		t.Errorf("A (no feature) tickets = %+v, want none", a.NoFeature.Tickets)
	}
	if len(a.Features) != 1 || a.Features[0].Key != "f1" || a.Features[0].Rounds != 2 {
		t.Fatalf("A features = %+v, want f1 with its ticketless round", a.Features)
	}
	if len(a.Features[0].Tickets) != 1 || a.Features[0].Tickets[0].Key != "t1" ||
		a.Features[0].Tickets[0].Rounds != 1 {
		t.Errorf("A/f1 tickets = %+v, want t1 with 1 round", a.Features[0].Tickets)
	}
	b := byKey["B"]
	if b.NoFeature.Rounds != 1 {
		t.Errorf("B NoFeature = %+v, want 1 round", b.NoFeature)
	}
	if len(b.Features) != 0 {
		t.Errorf("B features = %+v, want none (no labels)", b.Features)
	}
}

// checkGroupUnlabelledRepo pins the issue's "a repo with nothing to show
// expands to nothing": no labelled children, the whole count in (no feature).
func checkGroupUnlabelledRepo(t *testing.T, rep Report) {
	byKey := map[string]RepoRow{}
	for _, r := range rep.Repos {
		byKey[r.Key] = r
	}
	b := byKey["B"]
	if b.Rounds != 2 {
		t.Fatalf("repo B = %+v, want 2 rounds", b)
	}
	if len(b.Features) != 0 {
		t.Errorf("repo B features = %+v, want empty", b.Features)
	}
	if b.NoFeature.Rounds != 2 || len(b.NoFeature.Tickets) != 0 {
		t.Errorf("repo B (no feature) = %+v, want the whole 2 rounds and no tickets", b.NoFeature)
	}
}

// checkScratchFoldRow pins the fold's shape: one ScratchKey row holding the
// paths in Scratch, and the paths themselves gone from the top level.
func checkScratchFoldRow(t *testing.T, rep Report) {
	if len(rep.Repos) != 1 {
		t.Fatalf("Repos = %+v, want only the fold row", rep.Repos)
	}
	fold := rep.Repos[0]
	if fold.Key != ScratchKey {
		t.Fatalf("fold key = %q, want %q", fold.Key, ScratchKey)
	}
	if fold.Rounds != 2 {
		t.Errorf("fold Rounds = %d, want 2", fold.Rounds)
	}
	keys := make([]string, 0, len(fold.Scratch))
	for _, s := range fold.Scratch {
		keys = append(keys, s.Key)
	}
	if len(keys) != 2 || keys[0] != "/tmp/one" || keys[1] != "/tmp/two" {
		t.Fatalf("fold Scratch keys = %v, want /tmp/one and /tmp/two", keys)
	}
}

// checkScratchFoldTotals pins that the fold aggregates exactly as a plain group
// would: rounds, tokens, halts, commits and done summed, and bindings distinct
// across two children that share one.
func checkScratchFoldTotals(t *testing.T, rep Report) {
	if len(rep.Repos) != 1 {
		t.Fatalf("Repos = %+v, want only the fold row", rep.Repos)
	}
	fold := rep.Repos[0]
	if fold.Rounds != 2 || fold.Tokens != 30 {
		t.Errorf("fold = %+v, want 2 rounds and 30 tokens", fold.GroupRow)
	}
	if fold.Bindings != 1 {
		t.Errorf("fold Bindings = %d, want 1 (b1 in both repos)", fold.Bindings)
	}
	if fold.Landed != 1 {
		t.Errorf("fold Landed = %d, want 1", fold.Landed)
	}
	if fold.Halted != 1 {
		t.Errorf("fold Halted = %d, want 1 (the halted round)", fold.Halted)
	}
	if fold.Done != 1 || fold.ReportHalted != 1 {
		t.Errorf("fold Done/ReportHalted = %d/%d, want 1/1", fold.Done, fold.ReportHalted)
	}
	if fold.Commits != 3 {
		t.Errorf("fold Commits = %d, want 3 (2 + 1)", fold.Commits)
	}
	if len(fold.Scratch) != 2 {
		t.Fatalf("fold Scratch = %+v, want two repos", fold.Scratch)
	}
	if fold.Scratch[0].Tokens != 10 || fold.Scratch[1].Tokens != 20 {
		t.Errorf("child tokens = %d, %d, want 10 and 20", fold.Scratch[0].Tokens, fold.Scratch[1].Tokens)
	}
}

// checkScratchRemoteUnchanged pins that a remote repo and the "(none)" bucket
// keep their own rows, features and tickets beside the fold.
func checkScratchRemoteUnchanged(t *testing.T, rep Report) {
	byKey := map[string]RepoRow{}
	for _, r := range rep.Repos {
		byKey[r.Key] = r
	}
	fold, ok := byKey[ScratchKey]
	if !ok || fold.Rounds != 1 {
		t.Errorf("fold = %+v, want one round", fold)
	}
	remote := byKey["https://github.com/o/r"]
	if remote.Key != "https://github.com/o/r" || remote.Rounds != 1 || remote.Tokens != 100 {
		t.Errorf("remote repo = %+v, want its own key, 1 round and 100 tokens", remote)
	}
	if len(remote.Features) != 1 || remote.Features[0].Key != "f1" {
		t.Fatalf("remote features = %+v, want f1", remote.Features)
	}
	if len(remote.Features[0].Tickets) != 1 || remote.Features[0].Tickets[0].Key != "t1" {
		t.Errorf("remote f1 tickets = %+v, want t1", remote.Features[0].Tickets)
	}
	none := byKey["(none)"]
	if none.Rounds != 1 || none.Tokens != 0 {
		t.Errorf("(none) = %+v, want the one repo-less round", none)
	}
	if len(fold.Scratch) != 1 || fold.Scratch[0].Key != "/tmp/one" {
		t.Errorf("fold Scratch = %+v, want /tmp/one alone", fold.Scratch)
	}
}

// checkScratchNoFold pins that no scratch repo means no ScratchKey row and no
// Scratch children anywhere.
func checkScratchNoFold(t *testing.T, rep Report) {
	for _, r := range rep.Repos {
		if r.Key == ScratchKey {
			t.Errorf("Repos carries a fold row with no scratch repo: %+v", r)
		}
		if len(r.Scratch) != 0 {
			t.Errorf("repo %q has Scratch = %+v, want none", r.Key, r.Scratch)
		}
	}
	if len(rep.Repos) != 2 {
		t.Errorf("Repos = %+v, want the remote repo and (none)", rep.Repos)
	}
}

// checkScratchNoneNotFolded pins that "(none)" (a nil Repo) is excluded by the
// predicate itself, not by a branch: it keeps its own row beside the fold.
func checkScratchNoneNotFolded(t *testing.T, rep Report) {
	byKey := map[string]RepoRow{}
	for _, r := range rep.Repos {
		byKey[r.Key] = r
	}
	if len(rep.Repos) != 2 {
		t.Fatalf("Repos = %+v, want the fold and (none)", rep.Repos)
	}
	fold := byKey[ScratchKey]
	if fold.Rounds != 1 || len(fold.Scratch) != 1 || fold.Scratch[0].Key != "/tmp/one" {
		t.Errorf("fold = %+v, want only /tmp/one", fold)
	}
	none := byKey["(none)"]
	if none.Rounds != 1 || none.Tokens != 5 {
		t.Errorf("(none) = %+v, want its own one round and 5 tokens", none)
	}
}

// checkScratchChildFeatures pins that each child counts only its own feature
// rows, while the fold's own feature buckets stay zero.
func checkScratchChildFeatures(t *testing.T, rep Report) {
	if len(rep.Repos) != 1 {
		t.Fatalf("Repos = %+v, want only the fold row", rep.Repos)
	}
	fold := rep.Repos[0]
	if len(fold.Features) != 0 || fold.NoFeature.Rounds != 0 {
		t.Errorf("fold features = %+v and NoFeature = %+v, want both empty",
			fold.Features, fold.NoFeature)
	}
	if len(fold.Scratch) != 2 {
		t.Fatalf("fold Scratch = %+v, want two repos", fold.Scratch)
	}
	// groupRows order: rounds desc, then key asc, so /tmp/one leads with 2.
	one := fold.Scratch[0]
	if one.Key != "/tmp/one" || one.Rounds != 2 {
		t.Fatalf("first child = %+v, want /tmp/one with 2 rounds", one.GroupRow)
	}
	if len(one.Features) != 1 || one.Features[0].Key != "f1" || one.Features[0].Rounds != 1 {
		t.Errorf("one features = %+v, want f1 with its one round", one.Features)
	}
	if one.NoFeature.Rounds != 1 {
		t.Errorf("one NoFeature = %+v, want its one unlabelled round", one.NoFeature)
	}
	two := fold.Scratch[1]
	if two.Key != "/tmp/two" || two.Rounds != 1 || len(two.Features) != 1 ||
		two.Features[0].Tokens != 100 {
		t.Errorf("second child = %+v, want /tmp/two with f1 at 100 tokens", two)
	}
}

// checkGroupFeatureTickets pins that a feature's ticketless rounds count only
// in the feature, not in its ticket list.
func checkGroupFeatureTickets(t *testing.T, rep Report) {
	a := rep.Repos[0]
	if len(a.Features) != 1 || a.Features[0].Key != "f1" {
		t.Fatalf("A features = %+v, want f1", a.Features)
	}
	f := a.Features[0]
	if f.Rounds != 2 || f.Tokens != 15 {
		t.Errorf("A/f1 = %+v, want 2 rounds and 15 tokens", f)
	}
	if len(f.Tickets) != 1 || f.Tickets[0].Key != "t1" || f.Tickets[0].Rounds != 1 ||
		f.Tickets[0].Tokens != 10 {
		t.Errorf("A/f1 tickets = %+v, want t1 with its one labelled round", f.Tickets)
	}
	if a.NoFeature.Rounds != 0 {
		t.Errorf("A (no feature) = %+v, want zero: every round has a feature", a.NoFeature)
	}
}

// checkGroupNoFeatureTicket pins a labelled ticket on a featureless round: it
// nests under the repo's (no feature) bucket, not under a feature.
func checkGroupNoFeatureTicket(t *testing.T, rep Report) {
	a := rep.Repos[0]
	if len(a.Features) != 0 {
		t.Errorf("A features = %+v, want none", a.Features)
	}
	if a.NoFeature.Key != "(none)" || a.NoFeature.Rounds != 1 {
		t.Fatalf("A (no feature) = %+v, want 1 round keyed (none)", a.NoFeature)
	}
	if len(a.NoFeature.Tickets) != 1 || a.NoFeature.Tickets[0].Key != "t9" ||
		a.NoFeature.Tickets[0].Tokens != 7 {
		t.Errorf("A (no feature) tickets = %+v, want t9 with its round", a.NoFeature.Tickets)
	}
}

// checkGroupTicketAcrossFeatures pins a ticket one repo uses under two features:
// it appears under each, counting only that feature's rows.
func checkGroupTicketAcrossFeatures(t *testing.T, rep Report) {
	byKey := map[string]FeatureRow{}
	for _, f := range rep.Repos[0].Features {
		byKey[f.Key] = f
	}
	if len(byKey) != 2 {
		t.Fatalf("A features = %+v, want f1 and f2", rep.Repos[0].Features)
	}
	for key, want := range map[string]int64{"f1": 10, "f2": 100} {
		f := byKey[key]
		if len(f.Tickets) != 1 || f.Tickets[0].Key != "t1" || f.Tickets[0].Tokens != want {
			t.Errorf("%s tickets = %+v, want t1 with %d tokens", key, f.Tickets, want)
		}
	}
}

// checkGroupTicketAcrossRepos pins a ticket label two repos use: it appears
// under each repo's feature with only that repo's rows counted.
func checkGroupTicketAcrossRepos(t *testing.T, rep Report) {
	byKey := map[string]RepoRow{}
	for _, r := range rep.Repos {
		byKey[r.Key] = r
	}
	for key, want := range map[string]int64{"A": 10, "B": 100} {
		r := byKey[key]
		if len(r.Features) != 1 || len(r.Features[0].Tickets) != 1 ||
			r.Features[0].Tickets[0].Key != "t1" || r.Features[0].Tickets[0].Tokens != want {
			t.Errorf("%s features = %+v, want t1 with %d tokens", key, r.Features, want)
		}
	}
}

// checkGroupNoLabelsRepo pins a repo with no labels at all: no features, and
// (no feature) holds every round with no tickets.
func checkGroupNoLabelsRepo(t *testing.T, rep Report) {
	b := rep.Repos[0]
	if len(b.Features) != 0 {
		t.Errorf("repo B features = %+v, want none", b.Features)
	}
	if b.NoFeature.Key != "(none)" || b.NoFeature.Rounds != 2 || len(b.NoFeature.Tickets) != 0 {
		t.Errorf("repo B (no feature) = %+v, want every round and no tickets", b.NoFeature)
	}
}

// checkGroupNoRows pins that no rows builds no repos.
func checkGroupNoRows(t *testing.T, rep Report) {
	if len(rep.Repos) != 0 {
		t.Errorf("Repos = %+v, want none with no rows", rep.Repos)
	}
}

func checkGroupBreakdowns(t *testing.T, rep Report) {
	if len(rep.Repos) != 1 {
		t.Fatalf("repos = %+v, want one row", rep.Repos)
	}
	g := rep.Repos[0]
	if g.Bindings != 3 {
		t.Errorf("Bindings = %d, want 3 (b1, b2, b3)", g.Bindings)
	}
	if g.Done != 2 {
		t.Errorf("Done = %d, want 2 (the two done reports)", g.Done)
	}
	if g.ReportHalted != 1 {
		t.Errorf("ReportHalted = %d, want 1", g.ReportHalted)
	}
	if g.Commits != 3 {
		t.Errorf("Commits = %d, want 3 (2 + nil + 1 + 0)", g.Commits)
	}
	if got := g.ByCandidate; got["A"] != 2 || got["B"] != 1 || len(got) != 2 {
		t.Errorf("ByCandidate = %v, want A:2 B:1 (the nil candidate skipped)", got)
	}
}

func checkGroupTokens(t *testing.T, rep Report) {
	byKey := map[string]RepoRow{}
	for _, g := range rep.Repos {
		byKey[g.Key] = g
	}
	if got := byKey["A"].Tokens; got != 115 {
		t.Errorf("repo A Tokens = %d, want 115", got)
	}
	if got := byKey["B"].Tokens; got != 2 {
		t.Errorf("repo B Tokens = %d, want 2", got)
	}
	if got := byKey["(none)"].Tokens; got != 0 {
		t.Errorf("(none) Tokens = %d, want 0", got)
	}
	a := byKey["A"]
	if len(a.Features) != 1 || a.Features[0].Key != "f1" || a.Features[0].Tokens != 115 {
		t.Errorf("A/f1 = %+v, want 115 tokens", a.Features)
	}
	b := byKey["B"]
	if len(b.Features) != 1 || b.Features[0].Key != "f2" || b.Features[0].Tokens != 2 {
		t.Errorf("B/f2 = %+v, want 2 tokens", b.Features)
	}
}

// TestReliabilityWindowAndHours pins the window, the local-hour buckets and the
// active gates.
func TestReliabilityWindowAndHours(t *testing.T) {
	t.Parallel()

	loc := time.FixedZone("X", 2*3600)
	rows := []db.RoundRow{
		{Candidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 2},
		{Candidate: stStr("b"), Outcome: db.OutcomeReported},
	}
	gates := []availability.Gate{{Token: "a"}}
	hist := availability.History{Events: []availability.Event{
		// In the window: 01:30 UTC is 03:30 local.
		{At: time.Date(2026, 9, 24, 1, 30, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "google"},
		// After Until: excluded.
		{At: time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "google"},
		// Before Since: excluded.
		{At: time.Date(2026, 9, 19, 23, 0, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "google"},
		// In the window: 08:00 UTC is 10:00 local.
		{At: time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "openai"},
		// In the window.
		{At: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC), Kind: availability.SpawnFailed, Provider: "google"},
		// Before Since: excluded.
		{At: time.Date(2026, 9, 19, 23, 0, 0, 0, time.UTC), Kind: availability.SpawnFailed, Provider: "openai"},
	}}
	rep := Build(Inputs{
		Rows:    rows,
		History: hist,
		Gates:   gates,
		Since:   time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Until:   stNow,
		Loc:     loc,
	})

	if rep.Reliability.RateLimits != 2 {
		t.Errorf("rate limits = %d, want 2", rep.Reliability.RateLimits)
	}
	if rep.Reliability.SpawnFailures != 1 {
		t.Errorf("spawn failures = %d, want 1", rep.Reliability.SpawnFailures)
	}
	if rep.Reliability.Switches != 2 || rep.Reliability.RoundsSwitched != 1 {
		t.Errorf("switches = %d in %d rounds, want 2 in 1", rep.Reliability.Switches, rep.Reliability.RoundsSwitched)
	}
	if rep.Reliability.SwitchPct != 50 {
		t.Errorf("switch pct = %v, want 50", rep.Reliability.SwitchPct)
	}
	if len(rep.Reliability.ByHour) != 2 {
		t.Fatalf("by hour = %+v, want google and openai", rep.Reliability.ByHour)
	}
	if rep.Reliability.ByHour[0].Provider != "google" || rep.Reliability.ByHour[0].Counts[3] != 1 {
		t.Errorf("google = %+v, want one at local hour 3", rep.Reliability.ByHour[0])
	}
	if rep.Reliability.ByHour[1].Provider != "openai" || rep.Reliability.ByHour[1].Counts[10] != 1 {
		t.Errorf("openai = %+v, want one at local hour 10", rep.Reliability.ByHour[1])
	}
	if len(rep.Reliability.Active) != 1 || rep.Reliability.Active[0].Token != "a" {
		t.Errorf("active = %+v, want the gate passed through", rep.Reliability.Active)
	}
}

// TestOutcomes pins that the unstructured report outcome counts as "no outcome".
func TestOutcomes(t *testing.T) {
	t.Parallel()

	rows := []db.RoundRow{
		{Outcome: db.OutcomeReported, ReportOutcome: stStr("done")},
		{Outcome: db.OutcomeReported, ReportOutcome: stStr("unstructured")},
		{Outcome: db.OutcomeOpen},
		{Outcome: db.OutcomeHalted, ReportOutcome: stStr("halted")},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	if got := rep.Outcomes.ByReport["done"]; got != 1 {
		t.Errorf("reports done = %d, want 1", got)
	}
	if got := rep.Outcomes.ByReport["no outcome"]; got != 1 {
		t.Errorf("reports no outcome = %d, want 1 (the unstructured row)", got)
	}
	if got := rep.Outcomes.ByRound[db.OutcomeReported]; got != 2 {
		t.Errorf("rounds reported = %d, want 2", got)
	}
	if got := rep.Outcomes.ByRound[db.OutcomeOpen]; got != 1 {
		t.Errorf("rounds open = %d, want 1", got)
	}
}

// TestBuildEmpty pins that no rows builds a zero report without a panic.
func TestBuildEmpty(t *testing.T) {
	t.Parallel()

	rep := Build(Inputs{Until: stNow, Loc: time.UTC})

	if rep.Totals.Rounds != 0 || rep.Totals.CostUSD != 0 || len(rep.Scorecard) != 0 {
		t.Errorf("empty report = %+v, want zero totals and no scorecard", rep)
	}
	if !rep.Since.Equal(stNow) {
		t.Errorf("Since = %v, want Until when there is no row", rep.Since)
	}
	if len(rep.Outcomes.ByRound) != 0 || len(rep.Outcomes.ByReport) != 0 {
		t.Errorf("outcomes = %+v, want empty maps", rep.Outcomes)
	}
}

// TestBuildUndatedRowsKeepSinceAtUntil pins that an undated row does not pull
// Since back to year 1 and allocate a spend day per day since then.
func TestBuildUndatedRowsKeepSinceAtUntil(t *testing.T) {
	t.Parallel()

	rows := []db.RoundRow{{Outcome: db.OutcomeReported}, {Outcome: db.OutcomeOpen}}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	if !rep.Since.Equal(stNow) {
		t.Errorf("Since = %v, want Until when no row has a StartedAt", rep.Since)
	}
	if len(rep.Spend.Days) != 1 {
		t.Errorf("len(Spend.Days) = %d, want 1", len(rep.Spend.Days))
	}

	dated := stNow.Add(-48 * time.Hour)
	rows = append(rows, db.RoundRow{Outcome: db.OutcomeReported, StartedAt: dated})
	if got := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC}).Since; !got.Equal(dated) {
		t.Errorf("Since = %v, want the dated row's StartedAt %v", got, dated)
	}
}

// TestTokenKindsSumAndCache pins TokenCounts over every row and the two ok
// results that guard a zero denominator.
func TestTokenKindsSumAndCache(t *testing.T) {
	t.Parallel()

	rows := []db.RoundRow{
		{InTokens: stI64(100), CacheTokens: stI64(900), WriteTokens: stI64(50), OutTokens: stI64(10)},
		{InTokens: stI64(100), CacheTokens: stI64(900)},
		// No token field at all: not measured.
		{Candidate: stStr("a")},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	k := rep.Totals.TokenKinds
	if k.In != 200 || k.Cache != 1800 || k.Write != 50 || k.Out != 10 {
		t.Errorf("TokenKinds = %+v, want In 200, Cache 1800, Write 50, Out 10", k)
	}
	if k.Measured != 2 {
		t.Errorf("Measured = %d, want 2 (the two rows with a token field)", k.Measured)
	}
	if k.Total() != 2060 {
		t.Errorf("Total = %d, want 2060", k.Total())
	}
	if rep.Totals.Tokens != k.Total() {
		t.Errorf("Totals.Tokens = %d, want TokenKinds.Total() = %d", rep.Totals.Tokens, k.Total())
	}
	if pct, ok := k.CachePct(); !ok || pct != 90 {
		t.Errorf("CachePct = %v, %v; want 90, true (1800 of 2000 input)", pct, ok)
	}
	if per, ok := k.PerRound(k.Out); !ok || per != 5 {
		t.Errorf("PerRound(Out) = %d, %v; want 5, true (10 over 2 measured)", per, ok)
	}
	if _, ok := (TokenCounts{Write: 5, Out: 5}).CachePct(); ok {
		t.Error("CachePct with zero input = ok true, want false")
	}
	if _, ok := (TokenCounts{}).PerRound(10); ok {
		t.Error("PerRound with zero measured = ok true, want false")
	}
}
