package view

import "testing"

// TestChainPendingSegmentNamesTheStrandedMembers pins the segment a chain row
// adds when a member still holds a payload nobody collected: one name, every
// name, and nothing at all when there is none -- the last is what keeps a row
// without a stranded member byte-identical to the one before this segment.
func TestChainPendingSegmentNamesTheStrandedMembers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		f    ChainFacts
		want string
	}{
		{
			name: "no stranded member leaves the segment off",
			f:    ChainFacts{Plan: 1, Plans: 1},
			want: "",
		},
		{
			name: "one stranded member is named alone",
			f:    ChainFacts{Plan: 1, Plans: 1, PendingMembers: []string{"x-rev"}},
			want: "pending on x-rev",
		},
		{
			name: "several stranded members are all named",
			f:    ChainFacts{Plan: 1, Plans: 1, PendingMembers: []string{"x-rev", "x-sec"}},
			want: "pending on x-rev, x-sec",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ChainPendingSegment(tc.f); got != tc.want {
				t.Errorf("ChainPendingSegment(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}

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

// TestChainSegmentFlowRunningAtARunStep pins a workflow chain's running
// segment: the step its engine is on, the round it awaits, and its position in
// the plan input.
func TestChainSegmentFlowRunningAtARunStep(t *testing.T) {
	t.Parallel()

	f := ChainFacts{Status: "running", StepAt: "building", Round: 2, PlanPos: 1, PlanTotal: 3}
	if got, want := ChainSegment(f), "building r2 · plans 1/3"; got != want {
		t.Errorf("ChainSegment(%+v) = %q, want %q", f, got, want)
	}
}

// TestChainSegmentFlowRunningWhileACheckIsAwaited pins the check-awaited
// variant: the segment names the check run, not a round.
func TestChainSegmentFlowRunningWhileACheckIsAwaited(t *testing.T) {
	t.Parallel()

	f := ChainFacts{Status: "running", StepAt: "reviewing", Round: 5, Check: true, PlanPos: 2, PlanTotal: 3}
	if got, want := ChainSegment(f), "reviewing check run 5 · plans 2/3"; got != want {
		t.Errorf("ChainSegment(%+v) = %q, want %q", f, got, want)
	}
}

// TestChainSegmentFlowOnATerminalChain pins the terminal rule: a halted chain
// keeps the step, while a done chain drops it and keeps only its plan position.
func TestChainSegmentFlowOnATerminalChain(t *testing.T) {
	t.Parallel()

	halted := ChainFacts{Status: "halted", StepAt: "building", Round: 2, PlanPos: 1, PlanTotal: 3}
	if got, want := ChainSegment(halted), "building r2 · plans 1/3"; got != want {
		t.Errorf("ChainSegment(%+v) = %q, want %q", halted, got, want)
	}
	done := ChainFacts{Status: "done", StepAt: "building", Round: 3, PlanPos: 3, PlanTotal: 3}
	if got, want := ChainSegment(done), "plans 3/3"; got != want {
		t.Errorf("ChainSegment(%+v) = %q, want %q", done, got, want)
	}
}

// TestChainSegmentFlowInTheSecurityPhasePastItsPlans pins the security-phase
// line: a fix step after the plans walk is exhausted reads the plan it is
// fixing, not the reset position that made it look like six fresh plans.
func TestChainSegmentFlowInTheSecurityPhasePastItsPlans(t *testing.T) {
	t.Parallel()

	f := ChainFacts{Status: "running", StepAt: "fix-build", Round: 9, PlanPos: 6, PlanTotal: 6}
	if got, want := ChainSegment(f), "fix-build r9 · plans 6/6"; got != want {
		t.Errorf("ChainSegment(%+v) = %q, want %q", f, got, want)
	}
}

// TestChainSegmentFlowWithoutPlans pins a task-only workflow: with no plan to
// report the segment is just the step and its round.
func TestChainSegmentFlowWithoutPlans(t *testing.T) {
	t.Parallel()

	f := ChainFacts{Status: "running", StepAt: "building", Round: 2}
	if got, want := ChainSegment(f), "building r2"; got != want {
		t.Errorf("ChainSegment(%+v) = %q, want %q", f, got, want)
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
