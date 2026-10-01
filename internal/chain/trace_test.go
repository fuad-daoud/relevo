package chain

import (
	"strings"
	"testing"
)

// TestChainTraceLineFormatsPlanPhaseStepMemberRound pins the one trace line's
// columns: plan i/N, the state the chain was in before the event, the closing
// member and its round, and the event's detail -- the trace's own wording,
// spacing included. A red gate reads `check red`, not the design's
// `check red after regate`: the plan of record supersedes that example.
func TestChainTraceLineFormatsPlanPhaseStepMemberRound(t *testing.T) {
	t.Parallel()

	line := TraceLine{
		Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepBuilding,
		Member: "x", Round: 3,
		Event:  Event{Kind: EventBuilderClosed, Gate: GateRed},
		Action: Action{Kind: ActionSend, Member: MemberReviewer, Seed: SeedReviewer},
	}
	const want = "plan 2/4  build    x r3       check red"
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
		name: "a red builder close",
		line: TraceLine{
			Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepBuilding,
			Member: "x", Round: 4,
			Event: Event{Kind: EventBuilderClosed, Gate: GateRed},
		},
		want: "plan 2/4  build    x r4       check red",
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
	{
		name: "a resume names the step it moved to",
		line: TraceLine{
			Plan: 1, Plans: 1, Phase: PhaseBuild, Step: StepBuilding,
			Member: "shop", Round: 2,
			Event:  Event{Kind: EventNeedsYou, Member: MemberBuilder, Round: 2, Reason: ResumeReason(StepReviewing)},
			Action: Action{Kind: ActionSend, Member: MemberReviewer, Seed: SeedReviewer},
		},
		want: "plan 1/1  build    shop r2    resumed -> review",
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

// TestTraceLinePrintsAHaltReasonOnce pins the needs-you de-duplication: a row
// whose detail column already is the event's reason prints that reason once,
// not twice, while a distinct event detail and action reason still both print.
func TestTraceLinePrintsAHaltReasonOnce(t *testing.T) {
	t.Parallel()

	const reason = "member shop could not start: boom"
	line := TraceLine{
		Plan: 1, Plans: 1, Phase: PhaseBuild, Step: StepBuilding,
		Member: "shop", Round: 2,
		Event:  Event{Kind: EventNeedsYou, Member: MemberBuilder, Round: 2, Reason: reason},
		Action: Action{Kind: ActionHalt, Reason: reason},
		Reason: reason,
	}
	got := line.Line()
	if n := strings.Count(got, reason); n != 1 {
		t.Errorf("Line() = %q, the reason appears %d times, want 1", got, n)
	}
	if want := "plan 1/1  build    shop r2    " + reason; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}

	// A distinct event detail and action reason are not the same string: both
	// still print.
	distinct := TraceLine{
		Plan: 2, Plans: 4, Phase: PhaseBuild, Step: StepReviewing,
		Member: "x-rev", Round: 2,
		Event:  Event{Kind: EventReviewerClosed},
		Action: Action{Kind: ActionHalt, Reason: "reviewer gave no verdict"},
		Reason: "reviewer gave no verdict",
	}
	if out := distinct.Line(); !strings.HasSuffix(out, "no verdict  reviewer gave no verdict") {
		t.Errorf("Line() = %q, want the detail and the reason both printed", out)
	}
}

// TestResumeReasonNamesTheStepItMovedTo pins the resume reason's vocabulary:
// every step renders in the trace's own word, and a step the state machine does
// not name has no word, so its reason ends at the arrow.
func TestResumeReasonNamesTheStepItMovedTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		step Step
		want string
	}{
		{StepBuilding, "resumed -> build"},
		{StepReviewing, "resumed -> review"},
		{StepCorrecting, "resumed -> correct"},
		{StepScanning, "resumed -> scan"},
		{StepPlanningFixes, "resumed -> planning"},
		{Step(""), "resumed -> "},
		{Step("mystery"), "resumed -> "},
	} {
		if got := ResumeReason(tc.step); got != tc.want {
			t.Errorf("ResumeReason(%q) = %q, want %q", tc.step, got, tc.want)
		}
		if got := tc.step.Word(); !strings.HasSuffix(tc.want, got) {
			t.Errorf("Step(%q).Word() = %q, want the reason's own word", tc.step, got)
		}
	}
}
