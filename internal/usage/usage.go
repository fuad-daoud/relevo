// Package usage reads what a round consumed from each harness's own record and
// folds it into one figure with its provenance. It knows harness record shapes and
// nothing else: no rounds, no bindings, no store.
package usage

import (
	"fmt"
	"sort"
)

// Basis is where the dollar figure came from -- provenance, not the truth of the
// bill.
type Basis string

const (
	Measured  Basis = "measured"  // the harness itself reported dollars
	Estimated Basis = "estimated" // relevo priced harness-reported tokens
	Unknown   Basis = "unknown"   // no record, no tokens, no price row, or no way to read
)

// Tokens are counts as the provider bills them. Out includes thinking and
// reasoning tokens; every provider bills those as output.
type Tokens struct {
	In         int64 `json:"in"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Out        int64 `json:"out"`
}

func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{In: t.In + o.In, CacheRead: t.CacheRead + o.CacheRead, CacheWrite: t.CacheWrite + o.CacheWrite, Out: t.Out + o.Out}
}

func (t Tokens) Total() int64 { return t.In + t.CacheRead + t.CacheWrite + t.Out }

// CacheRatio is the share of prompt tokens served from cache; 0 when there were
// no prompt tokens.
func (t Tokens) CacheRatio() float64 {
	prompt := t.In + t.CacheRead + t.CacheWrite
	if prompt == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(prompt)
}

// Cost is dollars with provenance. Plan marks a subscription lane: the printer
// says "plan", never "$0".
type Cost struct {
	USD   float64 `json:"usd"`
	Basis Basis   `json:"basis"`
	Plan  bool    `json:"plan,omitempty"`
}

// Usage is what one round consumed; the zero value (Basis "") is never written.
type Usage struct {
	Harness    string `json:"harness"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`       // the model most Out tokens went to
	DurationMS int64  `json:"duration_ms"` // the round's wall time, 0 when its start is unknown
	Tokens     Tokens `json:"tokens"`
	Cost       Cost   `json:"cost"`
	Samples    int    `json:"samples"` // how many records were folded
	// Step figures from the builder stream; zero when its stream does not show them.
	Steps            int   `json:"steps,omitempty"`
	ToolCalls        int   `json:"tool_calls,omitempty"`
	StepP50MS        int64 `json:"step_p50_ms,omitempty"`
	FirstOutputP50MS int64 `json:"first_output_p50_ms,omitempty"`
	// Note says why Unknown, or "n models" when more than one was seen.
	Note string `json:"note,omitempty"`
}

// Sample is one billed message as a reader found it. HasCost says USD came from the
// record; false means it carried tokens only.
type Sample struct {
	Provider string
	Model    string
	Tokens   Tokens
	USD      float64
	HasCost  bool
}

// Fold sums samples into a Usage. Tokens always sum. Cost, in order: no samples is
// Unknown with the given Note; every sample HasCost is Measured with the records'
// sum; otherwise each sample without HasCost is estimated from prices, and if any
// does not resolve the whole figure is Unknown, USD 0. plan is copied through.
func Fold(samples []Sample, prices Prices, plan bool, note string) Usage {
	u := Usage{Cost: Cost{Basis: Unknown, Plan: plan}, Note: note}
	if len(samples) == 0 {
		return u
	}
	u.Samples = len(samples)
	u.Note = ""

	var notes []string
	outByModel := map[string]int64{}
	providerByModel := map[string]string{}
	usd := 0.0
	basis := Measured
	for _, s := range samples {
		tok, clamped := clamp(s.Tokens)
		if clamped && !contains(notes, "negative count clamped to 0") {
			notes = append(notes, "negative count clamped to 0")
		}
		u.Tokens = u.Tokens.Add(tok)
		outByModel[s.Model] += tok.Out
		providerByModel[s.Model] = s.Provider
		switch {
		case s.HasCost:
			usd += s.USD
		case basis == Unknown:
			// already failed; keep summing tokens only
		default:
			est, ok := prices.Estimate(s.Provider, s.Model, tok)
			if !ok {
				basis = Unknown
				usd = 0
				notes = append(notes, fmt.Sprintf("no price for %s/%s", s.Provider, s.Model))
				continue
			}
			basis = Estimated
			usd += est
		}
	}
	if basis == Unknown {
		usd = 0 // a measured sample after an unpriced one must not leak a partial figure
	}
	u.Cost.Basis = basis
	u.Cost.USD = usd

	models := make([]string, 0, len(outByModel))
	for m := range outByModel {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		if outByModel[models[i]] != outByModel[models[j]] {
			return outByModel[models[i]] > outByModel[models[j]]
		}
		return models[i] < models[j]
	})
	u.Model = models[0]
	u.Provider = providerByModel[u.Model]
	if len(models) > 1 {
		notes = append(notes, fmt.Sprintf("%d models", len(models)))
	}
	u.Note = join(notes)
	return u
}

// Add folds o into u: one round's segments becoming one figure. Tokens always
// sum. The cost combines by the rule Fold states for its samples, applied to
// whole segments: a side that moved tokens must carry a bill -- measured or
// estimated, never a plan lane -- and two billed sides sum their dollars
// under the more cautious basis, estimated if either side is. A side that
// moved tokens without a bill -- an unreadable read, a tokens-only
// PriorTokens, a plan lane -- leaves the sum Unknown with USD 0, never the
// priced remainder. A side that moved no tokens adds nothing and poisons
// nothing; when o moved none, u's cost stands as it is, so folding a zero
// prior leaves the figures that stand. Every other field is u's.
func Add(u, o Usage) Usage {
	total := u
	total.Tokens = u.Tokens.Add(o.Tokens)
	moved := func(s Usage) bool { return s.Tokens != (Tokens{}) }
	billed := func(s Usage) bool {
		return !s.Cost.Plan && (s.Cost.Basis == Measured || s.Cost.Basis == Estimated)
	}
	bill := func(s Usage) Cost {
		if moved(s) && !billed(s) {
			return Cost{Basis: Unknown}
		}
		return s.Cost
	}
	switch {
	case !moved(o):
		// u's cost stands untouched.
	case !moved(u):
		total.Cost = bill(o)
	default:
		if !billed(u) || !billed(o) {
			total.Cost = Cost{Basis: Unknown}
		} else {
			basis := Measured
			if u.Cost.Basis == Estimated || o.Cost.Basis == Estimated {
				basis = Estimated
			}
			total.Cost = Cost{Basis: basis, USD: u.Cost.USD + o.Cost.USD}
		}
	}
	return total
}

func clamp(t Tokens) (Tokens, bool) {
	clamped := false
	fix := func(v *int64) {
		if *v < 0 {
			*v = 0
			clamped = true
		}
	}
	fix(&t.In)
	fix(&t.CacheRead)
	fix(&t.CacheWrite)
	fix(&t.Out)
	return t, clamped
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}
