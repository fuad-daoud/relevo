package chain

import "testing"

// TestChainTraceLineFormatsPlanPhaseStepMemberRound pins the one trace line's
// columns: plan i/N, the state the chain was in before the event, the closing
// member and its round, and the event's detail -- the shape the design's
// example uses, spacing included.
func TestChainTraceLineFormatsPlanPhaseStepMemberRound(t *testing.T) {
	t.Parallel()

	line := TraceLine{
		Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepBuilding,
		Member: "x", Round: 3,
		Event:  Event{Kind: EventBuilderClosed, Gate: GateRed},
		Action: Action{Kind: ActionSend, Member: MemberReviewer, Seed: SeedReviewer},
	}
	const want = "plan 2/4  build    x r3       check red after regate"
	if got := line.Line(); got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

// traceLineCases is the table TestTraceLineDetailWords walks: one close of
// each kind, plus the halting close whose reason trails the line.
var traceLineCases = []struct {
	name string
	line TraceLine
	want string
}{
	{
		name: "a green builder close",
		line: TraceLine{
			Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepBuilding,
			Member: "x", Round: 4,
			Event: Event{Kind: EventBuilderClosed, Gate: GateGreen},
		},
		want: "plan 2/4  build    x r4       check green",
	},
	{
		name: "a builder close with no check",
		line: TraceLine{
			Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepBuilding,
			Member: "x", Round: 4,
			Event: Event{Kind: EventBuilderClosed, Gate: GateNone},
		},
		want: "plan 2/4  build    x r4       no check",
	},
	{
		name: "a reviewer changes",
		line: TraceLine{
			Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepReviewing,
			Member: "x-rev", Round: 2,
			Event: Event{Kind: EventReviewerClosed, Verdict: VerdictChanges},
		},
		want: "plan 2/4  review   x-rev r2   changes",
	},
	{
		name: "a reviewer pass",
		line: TraceLine{
			Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepReviewing,
			Member: "x-rev", Round: 3,
			Event: Event{Kind: EventReviewerClosed, Verdict: VerdictPass},
		},
		want: "plan 2/4  review   x-rev r3   pass",
	},
	{
		name: "a reviewer with no verdict",
		line: TraceLine{
			Plan: 1, Plans: 1, Phase: PhaseBuild, Step: StepReviewing,
			Member: "x-rev", Round: 2,
			Event: Event{Kind: EventReviewerClosed},
		},
		want: "plan 1/1  review   x-rev r2   no verdict",
	},
	{
		name: "a correction plan",
		line: TraceLine{
			Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepCorrecting,
			Member: "x-plan", Round: 2,
			Event: Event{Kind: EventPlannerClosed, PlanPresent: true},
		},
		want: "plan 2/4  correct  x-plan r2  correction plan",
	},
	{
		name: "a fix plan",
		line: TraceLine{
			Plan: 1, Plans: 1, Phase: PhaseSecurity, Step: StepPlanningFixes,
			Member: "x-plan", Round: 1,
			Event: Event{Kind: EventPlannerClosed, PlanPresent: true},
		},
		want: "plan 1/1  planning x-plan r1  fix plan",
	},
	{
		name: "a security close with one finding",
		line: TraceLine{
			Plan: 1, Plans: 1, Phase: PhaseSecurity, Step: StepScanning,
			Member: "x-sec", Round: 1,
			Event: Event{Kind: EventSecurityClosed, Findings: 1, FindingsGiven: true},
		},
		want: "plan 1/1  scan     x-sec r1   1 finding",
	},
	{
		name: "a security close with no findings",
		line: TraceLine{
			Plan: 1, Plans: 1, Phase: PhaseSecurity, Step: StepScanning,
			Member: "x-sec", Round: 1,
			Event: Event{Kind: EventSecurityClosed, Findings: 0, FindingsGiven: true},
		},
		want: "plan 1/1  scan     x-sec r1   no findings",
	},
	{
		name: "a security close without a count",
		line: TraceLine{
			Plan: 1, Plans: 1, Phase: PhaseSecurity, Step: StepScanning,
			Member: "x-sec", Round: 1,
			Event: Event{Kind: EventSecurityClosed},
		},
		want: "plan 1/1  scan     x-sec r1   no findings given",
	},
	{
		name: "a stop trims the line to the state",
		line: TraceLine{
			Plan: 1, Plans: 2, Phase: PhaseBuild, Step: StepBuilding,
			Member: "x", Round: 5,
			Event: Event{Kind: EventStopped},
		},
		want: "plan 1/2  build    x r5       stopped",
	},
	{
		name: "a halt carries its reason",
		line: TraceLine{
			Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepReviewing,
			Member: "x-rev", Round: 2,
			Event:  Event{Kind: EventReviewerClosed},
			Action: Action{Kind: ActionHalt, Reason: "reviewer gave no verdict"},
			Reason: "reviewer gave no verdict",
		},
		want: "plan 2/4  review   x-rev r2   no verdict  reviewer gave no verdict",
	},
}

// TestTraceLineDetailWords pins the word each kind of close renders, the
// columns' padding and a halt's trailing reason, so the trace reads the same
// whatever advanced the chain.
func TestTraceLineDetailWords(t *testing.T) {
	t.Parallel()

	for _, tc := range traceLineCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.line.Line(); got != tc.want {
				t.Errorf("Line() = %q, want %q", got, tc.want)
			}
		})
	}
}
