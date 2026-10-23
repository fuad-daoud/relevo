package usage

import (
	"strings"
	"testing"
)

func TestTokensArithmetic(t *testing.T) {
	a := Tokens{In: 10, CacheRead: 20, CacheWrite: 30, Out: 40}
	b := Tokens{In: 1, CacheRead: 2, CacheWrite: 3, Out: 4}
	got := a.Add(b)
	if got != (Tokens{In: 11, CacheRead: 22, CacheWrite: 33, Out: 44}) {
		t.Errorf("Add = %+v", got)
	}
	if got.Total() != 110 {
		t.Errorf("Total = %d, want 110", got.Total())
	}
	if r := got.CacheRatio(); r < 0.333 || r > 0.334 {
		t.Errorf("CacheRatio = %v, want 1/3", r)
	}
	if (Tokens{Out: 5}).CacheRatio() != 0 {
		t.Error("CacheRatio with no prompt tokens must be 0, not NaN")
	}
}

func TestFold(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"no samples is unknown", foldNoSamples},
		{"all measured", foldAllMeasured},
		{"estimates from prices", foldEstimatesFromPrices},
		{"mixed measured and estimated", foldMixedMeasuredAndEstimated},
		{"unpriced model is unknown, never zero", foldUnpricedModelIsUnknown},
		{"picks the model with the most output", foldPicksModelWithMostOutput},
		{"carries the plan flag", foldCarriesPlanFlag},
		{"a negative count is clamped to zero and noted", foldNegativeCountIsClamped},
	}
	for _, c := range cases {
		t.Run(c.name, c.run)
	}
}

func foldNoSamples(t *testing.T) {
	u := Fold(nil, testPrices(), false, "no stream")
	if u.Cost.Basis != Unknown || u.Samples != 0 || u.Note != "no stream" {
		t.Errorf("Fold(nil) = %+v", u)
	}
	if u.Cost.USD != 0 {
		t.Errorf("USD = %v, want 0", u.Cost.USD)
	}
}

func foldAllMeasured(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 100, Out: 10}, USD: 0.02, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 50, Out: 5}, USD: 0.01, HasCost: true},
	}
	u := Fold(s, Prices{}, false, "")
	if u.Cost.Basis != Measured {
		t.Fatalf("basis = %q, want measured", u.Cost.Basis)
	}
	if u.Cost.USD < 0.0299 || u.Cost.USD > 0.0301 {
		t.Errorf("USD = %v, want 0.03", u.Cost.USD)
	}
	if u.Tokens != (Tokens{In: 150, Out: 15}) || u.Samples != 2 {
		t.Errorf("tokens/samples = %+v/%d", u.Tokens, u.Samples)
	}
	if u.Model != "claude-sonnet-5" || u.Provider != "anthropic" {
		t.Errorf("model/provider = %q/%q", u.Model, u.Provider)
	}
	if u.Note != "" {
		t.Errorf("Note = %q, want empty for a single model", u.Note)
	}
}

func foldEstimatesFromPrices(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5",
		Tokens: Tokens{In: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000, Out: 1_000_000}}}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Estimated {
		t.Fatalf("basis = %q, want estimated", u.Cost.Basis)
	}
	if u.Cost.USD < 22.049 || u.Cost.USD > 22.051 { // 3 + 0.3 + 3.75 + 15
		t.Errorf("USD = %v, want 22.05", u.Cost.USD)
	}
}

func foldMixedMeasuredAndEstimated(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1_000_000}, USD: 5, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1_000_000}},
	}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Estimated {
		t.Fatalf("basis = %q, want estimated when any sample is estimated", u.Cost.Basis)
	}
	if u.Cost.USD < 7.999 || u.Cost.USD > 8.001 { // 5 measured + 3 estimated
		t.Errorf("USD = %v, want 8", u.Cost.USD)
	}
}

func foldUnpricedModelIsUnknown(t *testing.T) {
	s := []Sample{{Provider: "google", Model: "gemini-9-ultra", Tokens: Tokens{In: 500, Out: 20}}}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Unknown {
		t.Fatalf("basis = %q, want unknown for an unpriced model", u.Cost.Basis)
	}
	if u.Cost.USD != 0 {
		t.Errorf("USD = %v, want 0 under unknown", u.Cost.USD)
	}
	if !strings.Contains(u.Note, "no price for google/gemini-9-ultra") {
		t.Errorf("Note = %q, want the missing row named", u.Note)
	}
	if u.Tokens != (Tokens{In: 500, Out: 20}) {
		t.Errorf("tokens must still be recorded under unknown: %+v", u.Tokens)
	}
}

func foldPicksModelWithMostOutput(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{Out: 10}, USD: 1, HasCost: true},
		{Provider: "anthropic", Model: "claude-opus-5", Tokens: Tokens{Out: 100}, USD: 1, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{Out: 20}, USD: 1, HasCost: true},
	}
	u := Fold(s, Prices{}, false, "")
	if u.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the model with most Out tokens", u.Model)
	}
	if u.Note != "2 models" {
		t.Errorf("Note = %q, want \"2 models\"", u.Note)
	}
}

func foldCarriesPlanFlag(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1}, USD: 0, HasCost: true}}
	u := Fold(s, Prices{}, true, "")
	if !u.Cost.Plan {
		t.Error("Plan must be copied through")
	}
	if u.Cost.Basis != Measured {
		t.Errorf("basis = %q; a plan lane with a measured 0 stays measured, the printer says plan", u.Cost.Basis)
	}
}

func foldNegativeCountIsClamped(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: -5, Out: 3}, USD: 1, HasCost: true}}
	u := Fold(s, Prices{}, false, "")
	if u.Tokens.In != 0 || u.Tokens.Out != 3 {
		t.Errorf("tokens = %+v, want the negative clamped", u.Tokens)
	}
	if !strings.Contains(u.Note, "negative") {
		t.Errorf("Note = %q, want the clamp mentioned", u.Note)
	}
}

// TestAddCosts pins how two segments' costs combine: measured and
// estimated sum their dollars under the more cautious basis, a side that
// moved tokens without a bill poisons the sum to unknown/0, and a side that
// moved no tokens is a no-op.
func TestAddCosts(t *testing.T) {
	// measured + measured stays measured, dollars sum, tokens always sum.
	got := Add(
		Usage{Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.25, Basis: Measured}},
		Usage{Tokens: Tokens{In: 200, Out: 30}, Cost: Cost{USD: 0.40, Basis: Measured}},
	)
	if got.Tokens != (Tokens{In: 300, Out: 80}) {
		t.Errorf("tokens = %+v, want the sum", got.Tokens)
	}
	if got.Cost.Basis != Measured || got.Cost.USD != 0.65 {
		t.Errorf("cost = %+v, want measured 0.65", got.Cost)
	}

	// measured + estimated is estimated -- an estimate must not hide inside
	// a measurement -- with the dollars summed.
	got = Add(
		Usage{Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.25, Basis: Measured}},
		Usage{Tokens: Tokens{In: 200, Out: 30}, Cost: Cost{USD: 0.40, Basis: Estimated}},
	)
	if got.Cost.Basis != Estimated || got.Cost.USD != 0.65 {
		t.Errorf("cost = %+v, want estimated 0.65", got.Cost)
	}

	// A side that moved tokens without a bill -- unknown basis -- leaves the
	// sum unknown with USD 0, never the priced remainder; either order.
	got = Add(
		Usage{Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.25, Basis: Measured}},
		Usage{Tokens: Tokens{In: 400, Out: 150}},
	)
	if got.Cost.Basis != Unknown || got.Cost.USD != 0 {
		t.Errorf("cost = %+v, want unknown 0 with the prior unbilled", got.Cost)
	}
	if got.Tokens != (Tokens{In: 500, Out: 200}) {
		t.Errorf("tokens = %+v, want the sum under unknown", got.Tokens)
	}
	got = Add(
		Usage{Tokens: Tokens{In: 400, Out: 150}},
		Usage{Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.25, Basis: Measured}},
	)
	if got.Cost.Basis != Unknown || got.Cost.USD != 0 {
		t.Errorf("cost = %+v, want unknown 0 with the report unbilled", got.Cost)
	}

	// A zero-token unbilled side is a no-op: it adds nothing and poisons
	// nothing.
	got = Add(
		Usage{Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.25, Basis: Measured}},
		Usage{},
	)
	if got.Tokens != (Tokens{In: 100, Out: 50}) || got.Cost.Basis != Measured || got.Cost.USD != 0.25 {
		t.Errorf("Add with an empty side = %+v, want the first side unchanged", got)
	}
	got = Add(
		Usage{Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.25, Basis: Measured}},
		Usage{Cost: Cost{Basis: Unknown}},
	)
	if got.Tokens != (Tokens{In: 100, Out: 50}) || got.Cost.Basis != Measured || got.Cost.USD != 0.25 {
		t.Errorf("Add with a zero-token unknown side = %+v, want the first side unchanged", got)
	}
}

// TestAddPlanLane pins that a plan-lane segment keeps its cost when no other
// segment moved tokens, and poisons the sum once a second one did.
func TestAddPlanLane(t *testing.T) {
	plan := Usage{Tokens: Tokens{In: 200, Out: 30}, Cost: Cost{USD: 0.40, Basis: Measured, Plan: true}}
	billed := Usage{Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.25, Basis: Measured}}

	got := Add(plan, Usage{})
	if got != plan {
		t.Errorf("Add(plan, zero) = %+v, want the plan segment unchanged", got)
	}

	got = Add(plan, billed)
	if got.Cost.Basis != Unknown || got.Cost.USD != 0 || got.Tokens != (Tokens{In: 300, Out: 80}) {
		t.Errorf("Add(plan, billed) = %+v, want unknown/0 with summed tokens", got)
	}

	got = Add(billed, plan)
	if got.Cost.Basis != Unknown || got.Cost.USD != 0 || got.Tokens != (Tokens{In: 300, Out: 80}) {
		t.Errorf("Add(billed, plan) = %+v, want unknown/0 with summed tokens", got)
	}
}
