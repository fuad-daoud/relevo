package usage

import "testing"

func TestSumRoutesByBasis(t *testing.T) {
	us := []Usage{
		{Tokens: Tokens{In: 10}, Cost: Cost{USD: 1.00, Basis: Measured}},
		{Tokens: Tokens{In: 20}, Cost: Cost{USD: 0.25, Basis: Estimated}},
		{Tokens: Tokens{In: 30}, Cost: Cost{USD: 0.15, Basis: Estimated}},
		{Tokens: Tokens{In: 40}, Cost: Cost{USD: 0, Basis: Unknown}},
		{Tokens: Tokens{In: 50}, Cost: Cost{USD: 9.99, Basis: Measured, Plan: true}},
	}
	s := Sum(us, []bool{false, false, true, false, false})
	if s.Rounds != 4 || s.Consults != 1 {
		t.Errorf("rounds/consults = %d/%d, want 4/1", s.Rounds, s.Consults)
	}
	if s.Measured != 1.00 {
		t.Errorf("Measured = %v, want 1.00 (plan dollars excluded)", s.Measured)
	}
	if s.Estimated < 0.399 || s.Estimated > 0.401 {
		t.Errorf("Estimated = %v, want 0.40", s.Estimated)
	}
	if s.Plan != 1 || s.Unknown != 1 {
		t.Errorf("plan/unknown = %d/%d, want 1/1", s.Plan, s.Unknown)
	}
	if s.Tokens.In != 150 {
		t.Errorf("tokens must sum regardless of basis: %+v", s.Tokens)
	}
}

func TestSumPlanIsNotCash(t *testing.T) {
	s := Sum([]Usage{{Cost: Cost{USD: 5, Basis: Measured, Plan: true}}}, nil)
	if s.Measured != 0 || s.Estimated != 0 || s.Plan != 1 {
		t.Errorf("a plan round is a count, never dollars: %+v", s)
	}
}

func TestSumNilIsConsult(t *testing.T) {
	s := Sum([]Usage{{Cost: Cost{Basis: Unknown}}, {Cost: Cost{Basis: Unknown}}}, nil)
	if s.Rounds != 2 || s.Consults != 0 {
		t.Errorf("nil isConsult means every entry is a round: %+v", s)
	}
}

func TestSumStepsAndToolCalls(t *testing.T) {
	s := Sum([]Usage{{Steps: 3, ToolCalls: 4}, {Steps: 2, ToolCalls: 1, Cost: Cost{Basis: Unknown}}}, nil)
	if s.Steps != 5 || s.ToolCalls != 5 {
		t.Errorf("Sum Steps/ToolCalls = %d/%d, want 5/5", s.Steps, s.ToolCalls)
	}
	got := Spend{Steps: 1, ToolCalls: 2}.Add(Spend{Steps: 3, ToolCalls: 4})
	if got.Steps != 4 || got.ToolCalls != 6 {
		t.Errorf("Add Steps/ToolCalls = %d/%d, want 4/6", got.Steps, got.ToolCalls)
	}
}

func TestSpendAdd(t *testing.T) {
	a := Spend{Rounds: 1, Measured: 1, Tokens: Tokens{In: 1}}
	b := Spend{Rounds: 2, Consults: 1, Estimated: 2, Plan: 1, Unknown: 1, Tokens: Tokens{Out: 2}}
	got := a.Add(b)
	want := Spend{Rounds: 3, Consults: 1, Measured: 1, Estimated: 2, Plan: 1, Unknown: 1, Tokens: Tokens{In: 1, Out: 2}}
	if got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

func TestSpendLine(t *testing.T) {
	cases := []struct {
		s    Spend
		want string
	}{
		{Spend{Rounds: 4, Consults: 2, Measured: 1.23, Estimated: 0.40, Plan: 1, Unknown: 2}, "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown"},
		{Spend{Rounds: 4, Consults: 2, Measured: 1.23, Estimated: 0.40, Plan: 1, Unknown: 2, Tokens: Tokens{In: 2_100_000}}, "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown · 2.1M tok"},
		{Spend{Rounds: 1, Measured: 0.41}, "1 round · $0.41"},
		{Spend{Rounds: 1, Measured: 0.30}, "1 round · $0.30"},
		{Spend{Rounds: 3, Unknown: 3}, "3 rounds · 3 unknown"},
		{Spend{Consults: 1, Estimated: 0.02}, "0 rounds +1c · ~$0.02"},
		{Spend{}, "no rounds"},
		{Spend{Rounds: 1, Measured: 0.004}, "1 round · <$0.01"},
	}
	for _, c := range cases {
		if got := SpendLine(c.s); got != c.want {
			t.Errorf("SpendLine(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestMoneyShort(t *testing.T) {
	cases := []struct {
		s    Spend
		want string
	}{
		{Spend{Rounds: 4, Measured: 1.23, Estimated: 0.40, Unknown: 2}, "$1.23 · ~$0.40 · 2 unknown"},
		{Spend{Rounds: 4, Measured: 1.51, Tokens: Tokens{In: 2_100_000}}, "$1.51 · 2.1M tok"},
		{Spend{Rounds: 2, Unknown: 2, Tokens: Tokens{In: 206_000}}, "2 unknown · 206k tok"},
		{Spend{Rounds: 2, Plan: 2}, "2 plan"},
		{Spend{Rounds: 2}, ""},
		{Spend{}, ""},
	}
	for _, c := range cases {
		if got := MoneyShort(c.s); got != c.want {
			t.Errorf("MoneyShort(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestSpendAddSegmentCountsNoRound(t *testing.T) {
	base := Spend{
		Rounds: 2, Consults: 1, Steps: 5, ToolCalls: 4,
		Measured: 1.00, Estimated: 0.50, Plan: 1, Unknown: 1,
		Tokens: Tokens{In: 100, Out: 50},
	}

	// A measured segment adds tokens, steps, calls and measured USD.
	s1 := base.AddSegment(Usage{
		Steps: 2, ToolCalls: 3, Tokens: Tokens{In: 40, Out: 20},
		Cost: Cost{USD: 0.25, Basis: Measured},
	})
	if s1.Rounds != 2 || s1.Consults != 1 || s1.Plan != 1 || s1.Unknown != 1 {
		t.Errorf("counts changed: rounds=%d consults=%d plan=%d unknown=%d, want 2/1/1/1",
			s1.Rounds, s1.Consults, s1.Plan, s1.Unknown)
	}
	if s1.Steps != 7 || s1.ToolCalls != 7 {
		t.Errorf("steps/calls = %d/%d, want 7/7", s1.Steps, s1.ToolCalls)
	}
	if s1.Measured != 1.25 || s1.Estimated != 0.50 {
		t.Errorf("measured/estimated = %v/%v, want 1.25/0.50", s1.Measured, s1.Estimated)
	}
	if s1.Tokens.In != 140 || s1.Tokens.Out != 70 {
		t.Errorf("tokens = %+v, want in:140 out:70", s1.Tokens)
	}

	// An estimated segment adds estimated USD.
	s2 := base.AddSegment(Usage{
		Steps: 1, Tokens: Tokens{In: 10},
		Cost: Cost{USD: 0.15, Basis: Estimated},
	})
	if s2.Estimated != 0.65 || s2.Measured != 1.00 {
		t.Errorf("estimated/measured = %v/%v, want 0.65/1.00", s2.Estimated, s2.Measured)
	}

	// A plan segment adds tokens/steps/calls only, never dollars or a count.
	s3 := base.AddSegment(Usage{
		Steps: 3, ToolCalls: 1, Tokens: Tokens{In: 30},
		Cost: Cost{USD: 5.00, Basis: Measured, Plan: true},
	})
	if s3.Plan != 1 || s3.Measured != 1.00 || s3.Estimated != 0.50 {
		t.Errorf("plan segment changed counts or dollars: %+v", s3)
	}
	if s3.Steps != 8 || s3.ToolCalls != 5 || s3.Tokens.In != 130 {
		t.Errorf("plan segment steps/calls/tokens mismatch: steps=%d calls=%d in=%d", s3.Steps, s3.ToolCalls, s3.Tokens.In)
	}

	// An unknown-basis segment adds no USD.
	s4 := base.AddSegment(Usage{
		Steps: 1, Tokens: Tokens{In: 5},
		Cost: Cost{Basis: Unknown},
	})
	if s4.Rounds != 2 || s4.Unknown != 1 || s4.Plan != 1 {
		t.Errorf("unknown segment changed counts: rounds=%d unknown=%d", s4.Rounds, s4.Unknown)
	}
	if s4.Measured != 1.00 || s4.Estimated != 0.50 {
		t.Errorf("unknown segment changed dollars: measured=%v estimated=%v", s4.Measured, s4.Estimated)
	}
}
