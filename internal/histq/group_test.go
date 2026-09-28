package histq

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// A = rows 1, 2, 7, 10; B = rows 3, 4, 9; C = rows 5, 6, 8. CostUSD sums only
// the rows whose basis is not "unknown" (B skips row 3's 3.00, C skips row 8's
// 1.50) and Unknown counts every such row.
func TestGroupByCandidateSums(t *testing.T) {
	groups := Group(fixtureRows(), AxisCandidate, fxLoc)
	if len(groups) != 3 {
		t.Fatalf("len(Group) = %d, want 3", len(groups))
	}

	want := []struct {
		key       string
		rounds    int
		reported  int
		halted    int
		exited    int
		switched  int
		doneNoRep int
		open      int
		commits   int
		tokens    int64
		cost      float64
		unknown   int
		lastDay   int
		lastHour  int
		numbers   []int
	}{
		{fxBuilderAgy, 4, 4, 0, 0, 0, 0, 0, 7, 6750, 7.75, 0, 20, 9, []int{1, 2, 4, 1}},
		{fxBuilderClaude, 3, 2, 1, 0, 0, 0, 0, 8, 5000, 4.00, 1, 20, 13, []int{3, 3, 1}},
		{fxBuilderOpencode, 3, 0, 1, 1, 0, 0, 1, 3, 4500, 0.25, 2, 20, 12, []int{2, 2, 3}},
	}
	for i, w := range want {
		g := groups[i]
		if g.Key != w.key {
			t.Errorf("groups[%d].Key = %q, want %q", i, g.Key, w.key)
			continue
		}
		if g.Rounds != w.rounds || g.Reported != w.reported || g.Halted != w.halted ||
			g.Exited != w.exited || g.Switched != w.switched ||
			g.DoneNoReport != w.doneNoRep || g.Open != w.open {
			t.Errorf("%s counters = rounds %d reported %d halted %d exited %d switched %d done_no_report %d open %d, want %d/%d/%d/%d/%d/%d/%d",
				g.Key, g.Rounds, g.Reported, g.Halted, g.Exited, g.Switched, g.DoneNoReport, g.Open,
				w.rounds, w.reported, w.halted, w.exited, w.switched, w.doneNoRep, w.open)
		}
		if g.Commits != w.commits {
			t.Errorf("%s Commits = %d, want %d", g.Key, g.Commits, w.commits)
		}
		if g.Tokens != w.tokens {
			t.Errorf("%s Tokens = %d, want %d", g.Key, g.Tokens, w.tokens)
		}
		if math.Abs(g.CostUSD-w.cost) > 1e-9 {
			t.Errorf("%s CostUSD = %v, want %v", g.Key, g.CostUSD, w.cost)
		}
		if g.Unknown != w.unknown {
			t.Errorf("%s Unknown = %d, want %d", g.Key, g.Unknown, w.unknown)
		}
		if wantLast := fxDay(w.lastDay, w.lastHour); !g.Last.Equal(wantLast) {
			t.Errorf("%s Last = %v, want %v", g.Key, g.Last, wantLast)
		}
		var numbers []int
		for _, r := range g.Rows {
			numbers = append(numbers, r.Number)
		}
		if fmt.Sprint(numbers) != fmt.Sprint(w.numbers) {
			t.Errorf("%s Rows numbers = %v, want %v (newest first)", g.Key, numbers, w.numbers)
		}
	}
}

func TestGroupByDayNewestFirst(t *testing.T) {
	groups := Group(fixtureRows(), AxisDay, fxLoc)
	if len(groups) != 2 {
		t.Fatalf("len(Group) = %d, want 2", len(groups))
	}
	if groups[0].Key != "2026-09-20" || groups[1].Key != "2026-09-19" {
		t.Fatalf("day keys = [%s %s], want [2026-09-20 2026-09-19]", groups[0].Key, groups[1].Key)
	}
	if groups[0].Rounds != 6 || groups[1].Rounds != 4 {
		t.Errorf("day rounds = %d/%d, want 6/4", groups[0].Rounds, groups[1].Rounds)
	}
	if math.Abs(groups[0].CostUSD-6.50) > 1e-9 || math.Abs(groups[1].CostUSD-5.50) > 1e-9 {
		t.Errorf("day costs = %v/%v, want 6.50/5.50", groups[0].CostUSD, groups[1].CostUSD)
	}
}

// Cost desc, then rounds desc, then key asc.
func TestGroupOrderCostThenRoundsThenKey(t *testing.T) {
	rows := []db.RoundRow{
		{BindingName: "X", CostUSD: fxFloat(1.00), CostBasis: fxStr("measured")},
		{BindingName: "Y", CostUSD: fxFloat(2.00), CostBasis: fxStr("measured")},
		{BindingName: "Z", CostUSD: fxFloat(1.00), CostBasis: fxStr("measured")},
		{BindingName: "W", CostUSD: fxFloat(0.50), CostBasis: fxStr("measured")},
		{BindingName: "W", CostUSD: fxFloat(0.50), CostBasis: fxStr("measured")},
	}
	groups := Group(rows, AxisBinding, fxLoc)
	var keys []string
	for _, g := range groups {
		keys = append(keys, g.Key)
	}
	if fmt.Sprint(keys) != fmt.Sprint([]string{"Y", "W", "X", "Z"}) {
		t.Errorf("order = %v, want [Y W X Z] (cost desc, rounds desc, key asc)", keys)
	}
}

func TestGroupNoneIsNil(t *testing.T) {
	if got := Group(fixtureRows(), AxisNone, fxLoc); got != nil {
		t.Errorf("Group(AxisNone) = %v, want nil", got)
	}
	if got := Group(fixtureRows(), Axis(""), fxLoc); got != nil {
		t.Errorf("Group(\"\") = %v, want nil", got)
	}
}

// Keys are compared order-insensitively; the ordering rules have their own tests.
func TestGroupAxisKeys(t *testing.T) {
	tests := []struct {
		by   Axis
		want []string
	}{
		{AxisBinding, []string{"api", "infra", "web"}},
		{AxisRepo, []string{fxRepoAPI, fxRepoWeb}},
		{AxisFeature, []string{"checkout", "search"}},
		{AxisTicket, []string{"-", "#42", "o/r#607"}},
		{AxisCandidate, []string{fxBuilderAgy, fxBuilderClaude, fxBuilderOpencode}},
		{AxisHarness, []string{"agy", "claude", "opencode"}},
		{AxisProvider, []string{"antigravity", "anthropic", "openai"}},
		{AxisModel, []string{"gpt", "opus", "sonnet"}},
		{AxisOutcome, []string{"exited", "halted", "open", "reported"}},
	}

	for _, tt := range tests {
		t.Run(string(tt.by), func(t *testing.T) {
			groups := Group(fixtureRows(), tt.by, fxLoc)
			got := make([]string, len(groups))
			for i, g := range groups {
				got[i] = g.Key
			}
			sort.Strings(got)
			want := append([]string(nil), tt.want...)
			sort.Strings(want)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("Group(%s) keys = %v, want %v", tt.by, got, want)
			}
		})
	}
}

func TestTotalsCounts(t *testing.T) {
	got := Totals(fixtureRows())
	want := Tiles{
		Rounds:           10,
		CostUSD:          12.00,
		Unknown:          3,
		Tokens:           16250,
		Halted:           2,
		Exited:           1,
		MedianDurationMS: 3_000_000, // 50 minutes, the middle of 10..90
		Bindings:         3,
		Builders:         3,
	}
	if got.Rounds != want.Rounds || got.Unknown != want.Unknown || got.Tokens != want.Tokens ||
		got.Halted != want.Halted || got.Exited != want.Exited ||
		got.MedianDurationMS != want.MedianDurationMS || got.Bindings != want.Bindings ||
		got.Builders != want.Builders {
		t.Errorf("Totals = %+v, want %+v", got, want)
	}
	if math.Abs(got.CostUSD-want.CostUSD) > 1e-9 {
		t.Errorf("Totals.CostUSD = %v, want %v", got.CostUSD, want.CostUSD)
	}
}

// TestGroupByActor regroups on the round's actor: the config name a runner
// plays, not the candidate it runs on.
func TestGroupByActor(t *testing.T) {
	rows := []db.RoundRow{
		{BindingName: "a", Actor: "builder", Candidate: fxStr("x/y/z")},
		{BindingName: "b", Actor: "designer", Candidate: fxStr("x/y/z")},
		{BindingName: "c", Actor: "designer", Candidate: fxStr("q/u/v")},
	}
	got := map[string]int{}
	for _, g := range Group(rows, AxisActor, fxLoc) {
		got[g.Key] = g.Rounds
	}
	want := map[string]int{"builder": 1, "designer": 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Group(by actor) = %v, want %v", got, want)
	}
}

func TestTotalsMedianOddEven(t *testing.T) {
	rows := func(ms ...int64) []db.RoundRow {
		out := make([]db.RoundRow, len(ms))
		for i, v := range ms {
			out[i] = db.RoundRow{BindingID: fmt.Sprintf("b%d", i), DurationMS: fxInt64(v)}
		}
		return out
	}
	cases := []struct {
		name string
		ms   []int64
		want int64
	}{
		{"none", nil, 0},
		{"odd", []int64{600_000}, 600_000},
		{"even sorted", []int64{600_000, 1_200_000}, 900_000},
		{"even unsorted", []int64{1_200_000, 600_000}, 900_000},
		{"odd many", []int64{5, 1, 3}, 3},
		{"even many", []int64{2_400_000, 600_000, 1_800_000, 1_200_000}, 1_500_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Totals(rows(c.ms...)).MedianDurationMS; got != c.want {
				t.Errorf("MedianDurationMS = %d, want %d", got, c.want)
			}
		})
	}
}
