package chain

import (
	"encoding/json"
	"testing"
)

// TestDecodeEventRejectsMalformedJSON pins the malformed-document rule: a
// stored event that is not JSON is an error, never a zero event.
func TestDecodeEventRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	if _, err := DecodeEvent("not json"); err == nil {
		t.Fatal("DecodeEvent(malformed): want an error")
	}
}

// TestEventEncodeRoundTripsEveryKind pins the legacy read side's event kinds:
// each kind encodes to a document DecodeEvent reads back with the kind intact.
func TestEventEncodeRoundTripsEveryKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []EventKind{
		EventBuilderClosed, EventReviewerClosed, EventPlannerClosed,
		EventSecurityClosed, EventNeedsYou, EventStopped,
	} {
		e := Event{Kind: kind, Member: MemberBuilder, Round: 1}
		enc := e.Encode()
		if !json.Valid([]byte(enc)) {
			t.Fatalf("Encode(%q) wrote invalid JSON: %q", kind, enc)
		}
		got, err := DecodeEvent(enc)
		if err != nil {
			t.Fatalf("DecodeEvent(%q): %v", kind, err)
		}
		if got.Kind != kind {
			t.Errorf("round trip of %q: got kind %q", kind, got.Kind)
		}
	}
}

// TestStepWordNamesEveryStep pins the state column's vocabulary: each step the
// state machine names has a short word, and any other step has none.
func TestStepWordNamesEveryStep(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		step Step
		want string
	}{
		{StepBuilding, "build"},
		{StepReviewing, "review"},
		{StepCorrecting, "correct"},
		{StepScanning, "scan"},
		{StepPlanningFixes, "planning"},
		{Step(""), ""},
		{Step("mystery"), ""},
	} {
		if got := tc.step.Word(); got != tc.want {
			t.Errorf("Step(%q).Word() = %q, want %q", tc.step, got, tc.want)
		}
	}
}

// TestTraceLineStateWordFallsBackFromTheStepToThePhase pins stateWord's two
// fallbacks: a step the state machine does not name prints its own text, and an
// empty step prints the phase, so a row is never wordless.
func TestTraceLineStateWordFallsBackFromTheStepToThePhase(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		line TraceLine
		want string
	}{
		{
			name: "an unnamed step prints its own text",
			line: TraceLine{Step: Step("mystery")},
			want: "mystery",
		},
		{
			name: "an empty step prints the phase",
			line: TraceLine{Phase: PhaseBuild},
			want: "build",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.line.stateWord(); got != tc.want {
				t.Errorf("stateWord() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTraceLineDetailFallsBackForNeedsYouAndAnUnknownKind pins the two detail
// branches the named cases miss: a needs-you close with no reason says so
// itself, and a kind the switch does not name prints the kind.
func TestTraceLineDetailFallsBackForNeedsYouAndAnUnknownKind(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		line TraceLine
		want string
	}{
		{
			name: "a needs-you close with no reason",
			line: TraceLine{Event: Event{Kind: EventNeedsYou}},
			want: "needs you",
		},
		{
			name: "a kind the switch does not name",
			line: TraceLine{Event: Event{Kind: EventKind("mystery")}},
			want: "mystery",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.line.detail(); got != tc.want {
				t.Errorf("detail() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFindingWordCounts pins a security close's count wording: none, exactly
// one, and any larger number pluralised.
func TestFindingWordCounts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "no findings"},
		{1, "1 finding"},
		{2, "2 findings"},
		{7, "7 findings"},
	} {
		if got := findingWord(tc.n); got != tc.want {
			t.Errorf("findingWord(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
