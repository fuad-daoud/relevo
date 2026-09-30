package chain

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

// awaited is a running chain stopped at one member's round and waiting on that
// member: the shape every transition case starts from.
func awaited(step Step, member string, round int) State {
	return State{
		Status:   StatusRunning,
		Phase:    PhaseBuild,
		Step:     step,
		Plan:     1,
		Plans:    2,
		Settings: Settings{MaxCorrections: 3},
		Awaiting: Awaiting{Member: member, Round: round},
	}
}

func assertHalt(t *testing.T, got State, act Action, want string) {
	t.Helper()
	if got.Status != StatusHalted {
		t.Fatalf("status: want %s, got %s", StatusHalted, got.Status)
	}
	if act.Kind != ActionHalt {
		t.Fatalf("action kind: want %s, got %s", ActionHalt, act.Kind)
	}
	if act.Reason != want || got.Reason != want {
		t.Fatalf("reason: want %q, got action %q state %q", want, act.Reason, got.Reason)
	}
}

func assertFinish(t *testing.T, got State, act Action) {
	t.Helper()
	if got.Status != StatusDone {
		t.Fatalf("status: want %s, got %s", StatusDone, got.Status)
	}
	if got.Phase != PhaseFinished {
		t.Fatalf("phase: want %s, got %s", PhaseFinished, got.Phase)
	}
	if act.Kind != ActionFinish {
		t.Fatalf("action kind: want %s, got %s", ActionFinish, act.Kind)
	}
}

func assertSend(t *testing.T, act Action, member string, seed SeedKind) {
	t.Helper()
	if act.Kind != ActionSend || act.Member != member || act.Seed != seed {
		t.Fatalf("action: want send to %s seed %q, got %+v", member, seed, act)
	}
}

func TestNextBuilderCloseGreenSeedsReviewer(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 1)
	got, act := Next(s, Event{
		Kind: EventBuilderClosed, Member: MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeDone, Gate: GateGreen,
	})
	if got.Step != StepReviewing {
		t.Fatalf("step: want %s, got %s", StepReviewing, got.Step)
	}
	if got.Awaiting.Member != MemberReviewer {
		t.Fatalf("awaited member: want %s, got %s", MemberReviewer, got.Awaiting.Member)
	}
	assertSend(t, act, MemberReviewer, SeedReviewer)
}

// TestNextBuilderCloseNoCheckSeedsReviewer pins that a done round with no check
// behaves as green does: the chain steps to reviewing and sends the reviewer
// seed, so "no check" is never a halt.
func TestNextBuilderCloseNoCheckSeedsReviewer(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 1)
	got, act := Next(s, Event{
		Kind: EventBuilderClosed, Member: MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeDone, Gate: GateNone,
	})
	if got.Step != StepReviewing {
		t.Fatalf("step: want %s, got %s", StepReviewing, got.Step)
	}
	if got.Awaiting.Member != MemberReviewer {
		t.Fatalf("awaited member: want %s, got %s", MemberReviewer, got.Awaiting.Member)
	}
	assertSend(t, act, MemberReviewer, SeedReviewer)
}

// TestNextSendZeroesTheAwaitingRound pins the guard send sets: the round is
// cleared along with the member, so a caller that forgets to fill the new
// round can never match a stale round the state held before the event.
func TestNextSendZeroesTheAwaitingRound(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 7)
	got, act := Next(s, Event{
		Kind: EventBuilderClosed, Member: MemberBuilder, Round: 7,
		Outcome: reporttail.OutcomeDone, Gate: GateGreen,
	})
	assertSend(t, act, MemberReviewer, SeedReviewer)
	if got.Awaiting.Round != 0 {
		t.Fatalf("awaited round: want 0 until the caller fills it, got %d", got.Awaiting.Round)
	}
}

func TestNextBuilderCloseRedSeedsReviewer(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 1)
	got, act := Next(s, Event{
		Kind: EventBuilderClosed, Member: MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeDone, Gate: GateRed,
	})
	if got.Step != StepReviewing {
		t.Fatalf("step: want %s, got %s", StepReviewing, got.Step)
	}
	assertSend(t, act, MemberReviewer, SeedReviewer)
}

func TestNextBuilderHaltedHalts(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 1)
	got, act := Next(s, Event{
		Kind: EventBuilderClosed, Member: MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeHalted, Reason: "gate red",
	})
	assertHalt(t, got, act, "builder halted on plan 1: gate red")
}

func TestNextBuilderBlockedHalts(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 1)
	got, act := Next(s, Event{
		Kind: EventBuilderClosed, Member: MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeBlocked, Reason: "waiting on a decision",
	})
	assertHalt(t, got, act, "builder halted on plan 1: waiting on a decision")
}

func TestNextBuilderUnstructuredHalts(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 1)
	got, act := Next(s, Event{
		Kind: EventBuilderClosed, Member: MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeUnstructured,
	})
	assertHalt(t, got, act, "builder halted on plan 1: ")
}

func TestNextReviewerPassAdvancesPlan(t *testing.T) {
	t.Parallel()
	s := awaited(StepReviewing, MemberReviewer, 2)
	got, act := Next(s, Event{
		Kind: EventReviewerClosed, Member: MemberReviewer, Round: 2, Verdict: VerdictPass,
	})
	if got.Plan != 2 {
		t.Fatalf("plan: want 2, got %d", got.Plan)
	}
	if got.Corrections != 0 {
		t.Fatalf("corrections: want 0, got %d", got.Corrections)
	}
	if got.Step != StepBuilding {
		t.Fatalf("step: want %s, got %s", StepBuilding, got.Step)
	}
	assertSend(t, act, MemberBuilder, "")
}

func TestNextReviewerPassLastPlanStartsSecurity(t *testing.T) {
	t.Parallel()
	s := awaited(StepReviewing, MemberReviewer, 2)
	s.Plan = 2
	s.Settings.Security = true
	got, act := Next(s, Event{
		Kind: EventReviewerClosed, Member: MemberReviewer, Round: 2, Verdict: VerdictPass,
	})
	if got.Step != StepScanning {
		t.Fatalf("step: want %s, got %s", StepScanning, got.Step)
	}
	if got.Phase != PhaseSecurity {
		t.Fatalf("phase: want %s, got %s", PhaseSecurity, got.Phase)
	}
	assertSend(t, act, MemberSecurity, SeedSecurity)
}

func TestNextReviewerPassLastPlanWithoutSecurityFinishes(t *testing.T) {
	t.Parallel()
	s := awaited(StepReviewing, MemberReviewer, 2)
	s.Plan = 2
	got, act := Next(s, Event{
		Kind: EventReviewerClosed, Member: MemberReviewer, Round: 2, Verdict: VerdictPass,
	})
	assertFinish(t, got, act)
}

func TestNextReviewerPassInSecurityFinishes(t *testing.T) {
	t.Parallel()
	s := awaited(StepReviewing, MemberReviewer, 3)
	s.Phase = PhaseSecurity
	s.Settings.Security = true
	// A build plan still "left": the security phase must not advance to it.
	got, act := Next(s, Event{
		Kind: EventReviewerClosed, Member: MemberReviewer, Round: 3, Verdict: VerdictPass,
	})
	assertFinish(t, got, act)
}

func TestNextReviewerChangesSeedsCorrection(t *testing.T) {
	t.Parallel()
	s := awaited(StepReviewing, MemberReviewer, 2)
	got, act := Next(s, Event{
		Kind: EventReviewerClosed, Member: MemberReviewer, Round: 2, Verdict: VerdictChanges,
	})
	if got.Step != StepCorrecting {
		t.Fatalf("step: want %s, got %s", StepCorrecting, got.Step)
	}
	if got.Corrections != 0 {
		t.Fatalf("corrections: want 0 before the plan lands, got %d", got.Corrections)
	}
	assertSend(t, act, MemberPlanner, SeedCorrection)
}

func TestNextReviewerChangesAtBudgetHalts(t *testing.T) {
	t.Parallel()
	s := awaited(StepReviewing, MemberReviewer, 2)
	s.Corrections = 3
	got, act := Next(s, Event{
		Kind: EventReviewerClosed, Member: MemberReviewer, Round: 2, Verdict: VerdictChanges,
	})
	assertHalt(t, got, act, "reviewer still wants changes after 3 corrections")

	s = awaited(StepReviewing, MemberReviewer, 2)
	s.Settings.MaxCorrections = 0
	got, act = Next(s, Event{
		Kind: EventReviewerClosed, Member: MemberReviewer, Round: 2, Verdict: VerdictChanges,
	})
	assertHalt(t, got, act, "reviewer still wants changes after 0 corrections")
}

func TestNextReviewerNoVerdictHalts(t *testing.T) {
	t.Parallel()
	for _, v := range []Verdict{"", "maybe"} {
		s := awaited(StepReviewing, MemberReviewer, 2)
		got, act := Next(s, Event{
			Kind: EventReviewerClosed, Member: MemberReviewer, Round: 2, Verdict: v,
		})
		assertHalt(t, got, act, "reviewer gave no verdict")
	}
}

func TestNextCorrectionPlanSendsBuilderAndCounts(t *testing.T) {
	t.Parallel()
	s := awaited(StepCorrecting, MemberPlanner, 2)
	got, act := Next(s, Event{
		Kind: EventPlannerClosed, Member: MemberPlanner, Round: 2, PlanPresent: true,
	})
	if got.Corrections != 1 {
		t.Fatalf("corrections: want 1, got %d", got.Corrections)
	}
	if got.Step != StepBuilding {
		t.Fatalf("step: want %s, got %s", StepBuilding, got.Step)
	}
	assertSend(t, act, MemberBuilder, "")
}

func TestNextCorrectionPlanMissingHalts(t *testing.T) {
	t.Parallel()
	s := awaited(StepCorrecting, MemberPlanner, 2)
	got, act := Next(s, Event{
		Kind: EventPlannerClosed, Member: MemberPlanner, Round: 2,
	})
	assertHalt(t, got, act, "planner wrote no plan")
}

func TestNextSecurityNoFindingsFinishes(t *testing.T) {
	t.Parallel()
	s := awaited(StepScanning, MemberSecurity, 1)
	s.Phase = PhaseSecurity
	got, act := Next(s, Event{
		Kind: EventSecurityClosed, Member: MemberSecurity, Round: 1,
		Findings: 0, FindingsGiven: true,
	})
	assertFinish(t, got, act)
}

func TestNextSecurityFindingsSeedFixPlanner(t *testing.T) {
	t.Parallel()
	s := awaited(StepScanning, MemberSecurity, 1)
	s.Phase = PhaseSecurity
	got, act := Next(s, Event{
		Kind: EventSecurityClosed, Member: MemberSecurity, Round: 1,
		Findings: 2, FindingsGiven: true,
	})
	if got.Step != StepPlanningFixes {
		t.Fatalf("step: want %s, got %s", StepPlanningFixes, got.Step)
	}
	assertSend(t, act, MemberPlanner, SeedFixes)
}

func TestNextSecurityWithoutACountHalts(t *testing.T) {
	t.Parallel()
	s := awaited(StepScanning, MemberSecurity, 1)
	s.Phase = PhaseSecurity
	got, act := Next(s, Event{
		Kind: EventSecurityClosed, Member: MemberSecurity, Round: 1,
	})
	assertHalt(t, got, act, "security gave no finding count")
}

func TestNextFixPlanSendsBuilderAndResetsCorrections(t *testing.T) {
	t.Parallel()
	s := awaited(StepPlanningFixes, MemberPlanner, 3)
	s.Phase = PhaseSecurity
	s.Corrections = 2
	got, act := Next(s, Event{
		Kind: EventPlannerClosed, Member: MemberPlanner, Round: 3, PlanPresent: true,
	})
	if got.Corrections != 0 {
		t.Fatalf("corrections: want 0, got %d", got.Corrections)
	}
	if got.Step != StepBuilding {
		t.Fatalf("step: want %s, got %s", StepBuilding, got.Step)
	}
	if got.Phase != PhaseSecurity {
		t.Fatalf("phase: want %s, got %s", PhaseSecurity, got.Phase)
	}
	assertSend(t, act, MemberBuilder, "")
}

func TestNextFixPlanMissingHalts(t *testing.T) {
	t.Parallel()
	s := awaited(StepPlanningFixes, MemberPlanner, 3)
	s.Phase = PhaseSecurity
	got, act := Next(s, Event{
		Kind: EventPlannerClosed, Member: MemberPlanner, Round: 3,
	})
	assertHalt(t, got, act, "planner wrote no plan")
}

func TestNextNeedsYouHaltsWithTheMembersReason(t *testing.T) {
	t.Parallel()
	const reason = "the harness asked for a decision"
	s := awaited(StepReviewing, MemberReviewer, 2)
	got, act := Next(s, Event{
		Kind: EventNeedsYou, Member: MemberReviewer, Round: 2, Reason: reason,
	})
	assertHalt(t, got, act, reason)
}

func TestNextStoppedStops(t *testing.T) {
	t.Parallel()
	s := awaited(StepBuilding, MemberBuilder, 1)
	got, act := Next(s, Event{
		Kind: EventStopped, Member: MemberBuilder, Round: 1,
	})
	if got.Status != StatusStopped {
		t.Fatalf("status: want %s, got %s", StatusStopped, got.Status)
	}
	if act.Kind != ActionStop {
		t.Fatalf("action kind: want %s, got %s", ActionStop, act.Kind)
	}
}

func TestNextIgnoresAnotherRound(t *testing.T) {
	t.Parallel()
	s := awaited(StepReviewing, MemberReviewer, 2)
	for _, e := range []Event{
		{Kind: EventReviewerClosed, Member: MemberReviewer, Round: 3, Verdict: VerdictPass},
		{Kind: EventReviewerClosed, Member: MemberPlanner, Round: 2, Verdict: VerdictPass},
		{Kind: EventStopped, Member: MemberBuilder, Round: 1},
	} {
		got, act := Next(s, e)
		if !reflect.DeepEqual(got, s) {
			t.Fatalf("event %+v changed the state: want %+v, got %+v", e, s, got)
		}
		if !reflect.DeepEqual(act, Action{}) {
			t.Fatalf("event %+v produced action %+v, want none", e, act)
		}
	}
}

func TestNextTerminalIsInert(t *testing.T) {
	t.Parallel()
	for _, st := range []Status{StatusHalted, StatusStopped, StatusDone} {
		s := awaited(StepBuilding, MemberBuilder, 1)
		s.Status = st
		got, act := Next(s, Event{
			Kind: EventBuilderClosed, Member: MemberBuilder, Round: 1,
			Outcome: reporttail.OutcomeDone, Gate: GateGreen,
		})
		if !reflect.DeepEqual(got, s) {
			t.Fatalf("status %s: want %+v, got %+v", st, s, got)
		}
		if !reflect.DeepEqual(act, Action{}) {
			t.Fatalf("status %s: action %+v, want none", st, act)
		}
	}
}

func TestEventRoundTripsThroughJSON(t *testing.T) {
	t.Parallel()
	e := Event{
		Kind: EventSecurityClosed, Member: MemberSecurity, Round: 4,
		Outcome: reporttail.OutcomeDone, Gate: GateRed, Verdict: VerdictChanges,
		PlanPresent: true, Findings: 2, FindingsGiven: true, Reason: "why",
	}
	enc := e.Encode()
	if !json.Valid([]byte(enc)) {
		t.Fatalf("Encode wrote invalid JSON: %q", enc)
	}
	got, err := DecodeEvent(enc)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if !reflect.DeepEqual(got, e) {
		t.Fatalf("round trip: want %+v, got %+v", e, got)
	}
}

func TestDecodeEventRejectsAnUnknownKind(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{`{"Kind":"bogus"}`, `{}`, `{"Kind":""}`} {
		if _, err := DecodeEvent(doc); err == nil {
			t.Fatalf("DecodeEvent(%s): want an error", doc)
		}
	}
}

func TestActionRoundTripsThroughJSON(t *testing.T) {
	t.Parallel()
	for _, a := range []Action{
		{Kind: ActionNone},
		{Kind: ActionSend, Member: MemberBuilder},
		{Kind: ActionSend, Member: MemberPlanner, Seed: SeedCorrection},
		{Kind: ActionHalt, Reason: "reviewer gave no verdict"},
		{Kind: ActionStop},
		{Kind: ActionFinish},
	} {
		enc := a.Encode()
		if !json.Valid([]byte(enc)) {
			t.Fatalf("Encode wrote invalid JSON: %q", enc)
		}
		got, err := DecodeAction(enc)
		if err != nil {
			t.Fatalf("DecodeAction(%s): %v", enc, err)
		}
		if !reflect.DeepEqual(got, a) {
			t.Fatalf("round trip: want %+v, got %+v", a, got)
		}
	}
}

func TestDecodeActionRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{"not json", `{"Kind":`, "", `{"Kind":"bogus"}`} {
		if _, err := DecodeAction(doc); err == nil {
			t.Fatalf("DecodeAction(%q): want an error", doc)
		}
	}
}
