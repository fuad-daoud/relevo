package view

import "testing"

func TestChainSegmentPlanPhaseCorrections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		f    ChainFacts
		want string
	}{
		{
			name: "no corrections omits the segment",
			f:    ChainFacts{Plan: 2, Plans: 4, Step: "reviewing"},
			want: "plan 2/4 · reviewing",
		},
		{
			name: "one correction is singular",
			f:    ChainFacts{Plan: 2, Plans: 4, Step: "reviewing", Corrections: 1},
			want: "plan 2/4 · reviewing · 1 correction",
		},
		{
			name: "several corrections pluralise",
			f:    ChainFacts{Plan: 1, Plans: 3, Step: "building", Corrections: 3},
			want: "plan 1/3 · building · 3 corrections",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ChainSegment(tc.f); got != tc.want {
				t.Errorf("ChainSegment(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}

// TestChainSegmentOmitsTheStepOnATerminalChain pins item 3: a done or stopped
// chain's segment drops the step -- the work is over -- while a halted chain
// keeps it and the correction rounds still print.
func TestChainSegmentOmitsTheStepOnATerminalChain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		f    ChainFacts
		want string
	}{
		{
			name: "a done chain drops the step",
			f:    ChainFacts{Status: "done", Plan: 3, Plans: 3, Step: "building"},
			want: "plan 3/3",
		},
		{
			name: "a stopped chain drops the step",
			f:    ChainFacts{Status: "stopped", Plan: 2, Plans: 4, Step: "reviewing"},
			want: "plan 2/4",
		},
		{
			name: "a halted chain keeps the step",
			f:    ChainFacts{Status: "halted", Plan: 2, Plans: 4, Step: "reviewing"},
			want: "plan 2/4 · reviewing",
		},
		{
			name: "corrections still print on a done chain",
			f:    ChainFacts{Status: "done", Plan: 3, Plans: 3, Step: "building", Corrections: 2},
			want: "plan 3/3 · 2 corrections",
		},
		{
			name: "a manual round replaces the whole segment",
			f:    ChainFacts{Status: "halted", Plan: 2, Plans: 4, Step: "reviewing", ManualRound: 3},
			want: "manual round 3 running",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ChainSegment(tc.f); got != tc.want {
				t.Errorf("ChainSegment(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}

func TestChainDisplayMapsEveryStatus(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status string
		want   string
	}{
		{status: "running", want: "ACTIVE"},
		{status: "halted", want: "NEEDS YOU"},
		{status: "stopped", want: "NEEDS YOU"},
		{status: "done", want: "DONE"},
	} {
		if got := ChainDisplay(ChainFacts{Status: tc.status}); got != tc.want {
			t.Errorf("ChainDisplay(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// TestChainDisplayHaltedWithAManualRound pins item 4's display word: a halted
// or stopped chain whose builder has an open round reads its own status word,
// not NEEDS YOU, so the row does not claim a human must act while work runs.
func TestChainDisplayHaltedWithAManualRound(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status string
		want   string
	}{
		{status: "halted", want: "HALTED"},
		{status: "stopped", want: "STOPPED"},
	} {
		if got := ChainDisplay(ChainFacts{Status: tc.status, ManualRound: 2}); got != tc.want {
			t.Errorf("ChainDisplay(%s, manual round) = %q, want %q", tc.status, got, tc.want)
		}
	}
}
