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
