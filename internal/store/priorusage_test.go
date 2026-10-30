package store

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestPriorTokensOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		round   int
		entries []LogEntry
		want    usage.Tokens
	}{
		{
			name:    "no switches",
			round:   1,
			entries: []LogEntry{{Round: 1, Kind: KindPrompt}},
			want:    usage.Tokens{},
		},
		{
			name:  "two switch entries in the round",
			round: 1,
			entries: []LogEntry{
				{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}},
				{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}}},
			},
			want: usage.Tokens{In: 300, Out: 80},
		},
		{
			name:  "switch entry in another round ignored",
			round: 1,
			entries: []LogEntry{
				{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}},
				{Round: 2, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 500, Out: 500}}},
			},
			want: usage.Tokens{In: 100, Out: 50},
		},
		{
			name:  "report with PriorTokens added",
			round: 1,
			entries: []LogEntry{
				{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}},
				{Round: 1, Direction: DirToMasterMind, Kind: KindReport, PriorTokens: &usage.Tokens{In: 400, Out: 150}},
			},
			want: usage.Tokens{In: 500, Out: 200},
		},
		{
			name:  "switch entry with nil Usage skipped",
			round: 1,
			entries: []LogEntry{
				{Round: 1, Kind: KindSwitch, Usage: nil},
				{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 150, Out: 25}}},
			},
			want: usage.Tokens{In: 150, Out: 25},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PriorTokensOf(tc.entries, tc.round)
			if got != tc.want {
				t.Errorf("PriorTokensOf() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// priorUsageTestCases is TestPriorUsageOf's table: PriorTokensOf's existing
// tokens cases, plus the cost combinations the row rule pins.
var priorUsageTestCases = []struct {
	name    string
	round   int
	entries []LogEntry
	want    usage.Usage
}{
	{
		name:    "no switches",
		round:   1,
		entries: []LogEntry{{Round: 1, Kind: KindPrompt}},
		want:    usage.Usage{},
	},
	{
		name:  "two switch entries in the round",
		round: 1,
		entries: []LogEntry{
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}, Cost: usage.Cost{USD: 0.40, Basis: usage.Measured}}},
		},
		want: usage.Usage{Tokens: usage.Tokens{In: 300, Out: 80}, Cost: usage.Cost{USD: 0.65, Basis: usage.Measured}},
	},
	{
		name:  "switch entry in another round ignored",
		round: 1,
		entries: []LogEntry{
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
			{Round: 2, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 500, Out: 500}, Cost: usage.Cost{USD: 5, Basis: usage.Measured}}},
		},
		want: usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}},
	},
	{
		name:  "report with PriorTokens added",
		round: 1,
		entries: []LogEntry{
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
			{Round: 1, Direction: DirToMasterMind, Kind: KindReport, PriorTokens: &usage.Tokens{In: 400, Out: 150}},
		},
		// PriorTokens moved tokens with no bill: the cost is unknown,
		// never the switch side's priced remainder.
		want: usage.Usage{Tokens: usage.Tokens{In: 500, Out: 200}, Cost: usage.Cost{Basis: usage.Unknown}},
	},
	{
		name:  "PriorTokens alone is tokens only, unknown cost",
		round: 1,
		entries: []LogEntry{
			{Round: 1, Direction: DirToMasterMind, Kind: KindReport, PriorTokens: &usage.Tokens{In: 400, Out: 150}},
		},
		want: usage.Usage{Tokens: usage.Tokens{In: 400, Out: 150}, Cost: usage.Cost{Basis: usage.Unknown}},
	},
	{
		name:  "switch entry with nil Usage skipped",
		round: 1,
		entries: []LogEntry{
			{Round: 1, Kind: KindSwitch, Usage: nil},
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 150, Out: 25}, Cost: usage.Cost{USD: 0.10, Basis: usage.Estimated}}},
		},
		want: usage.Usage{Tokens: usage.Tokens{In: 150, Out: 25}, Cost: usage.Cost{USD: 0.10, Basis: usage.Estimated}},
	},
	{
		name:  "mixed measured and estimated is estimated",
		round: 1,
		entries: []LogEntry{
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 200}, Cost: usage.Cost{USD: 0.40, Basis: usage.Estimated}}},
		},
		want: usage.Usage{Tokens: usage.Tokens{In: 300}, Cost: usage.Cost{USD: 0.65, Basis: usage.Estimated}},
	},
	{
		name:  "a switch that moved tokens without a bill poisons",
		round: 1,
		entries: []LogEntry{
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}},
			{Round: 1, Kind: KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}, Cost: usage.Cost{USD: 0.40, Basis: usage.Measured}}},
		},
		want: usage.Usage{Tokens: usage.Tokens{In: 300, Out: 80}, Cost: usage.Cost{Basis: usage.Unknown}},
	},
}

// TestPriorUsageOf is PriorTokensOf's rule with cost: the existing
// tokens cases, plus how the segments' bills combine -- measured and
// estimated sum under the more cautious basis, a segment that moved tokens
// without a bill leaves the cost unknown/0, a segment that moved none adds
// nothing.
func TestPriorUsageOf(t *testing.T) {
	t.Parallel()

	for _, tc := range priorUsageTestCases {
		t.Run(tc.name, func(t *testing.T) {
			got := PriorUsageOf(tc.entries, tc.round)
			if got != tc.want {
				t.Errorf("PriorUsageOf() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
