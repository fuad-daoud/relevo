package chain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

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

// TestBuilderHaltReasonPrefersHaltedAt pins the first source: a tail that names
// where the builder stopped is the reason, whatever else is set.
func TestBuilderHaltReasonPrefersHaltedAt(t *testing.T) {
	t.Parallel()

	tail := reporttail.Tail{HaltedAt: "step 3, the migration", NotDone: []string{"finish the tests"}}
	if got, want := BuilderHaltReason(tail, "gate=fail", "halted"), "halted_at: step 3, the migration"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}
}

// TestBuilderHaltReasonFallsBackToTheNote pins the second source: with no
// halted_at the close's own note is the reason.
func TestBuilderHaltReasonFallsBackToTheNote(t *testing.T) {
	t.Parallel()

	tail := reporttail.Tail{NotDone: []string{"finish the tests"}}
	if got, want := BuilderHaltReason(tail, "gate=fail", "halted"), "note: gate=fail"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}
}

// TestBuilderHaltReasonFallsBackToNotDone pins the third source: with neither
// halted_at nor a note, the first not_done item is the reason.
func TestBuilderHaltReasonFallsBackToNotDone(t *testing.T) {
	t.Parallel()

	tail := reporttail.Tail{NotDone: []string{"finish the tests", "then the docs"}}
	if got, want := BuilderHaltReason(tail, "", "blocked"), "not_done: finish the tests"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}
}

// TestBuilderHaltReasonSaysTheOutcomeWhenTheTailIsEmpty pins the fallback: a
// close with no source at all names its own outcome word rather than an empty
// label.
func TestBuilderHaltReasonSaysTheOutcomeWhenTheTailIsEmpty(t *testing.T) {
	t.Parallel()

	if got, want := BuilderHaltReason(reporttail.Tail{}, "", "halted"), "status: halted"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}
	if got, want := BuilderHaltReason(reporttail.Tail{}, "", "blocked"), "status: blocked"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}
}

// TestBuilderHaltReasonSkipsAnEmptySource pins the empty-source rule: a source
// that is present but blank is skipped, not rendered as an empty label.
func TestBuilderHaltReasonSkipsAnEmptySource(t *testing.T) {
	t.Parallel()

	tail := reporttail.Tail{HaltedAt: "   \n  ", NotDone: []string{""}}
	if got, want := BuilderHaltReason(tail, "", "deferred"), "status: deferred"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}
}

// TestBuilderHaltReasonCapsToOneLine pins the one-line rule: a value carrying a
// newline contributes only its first line.
func TestBuilderHaltReasonCapsToOneLine(t *testing.T) {
	t.Parallel()

	tail := reporttail.Tail{HaltedAt: "waiting on a decision\nand then some more detail"}
	if got, want := BuilderHaltReason(tail, "", "halted"), "halted_at: waiting on a decision"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}

	// The note is capped the same way.
	if got, want := BuilderHaltReason(reporttail.Tail{}, "first line\nsecond line", "halted"), "note: first line"; got != want {
		t.Errorf("BuilderHaltReason = %q, want %q", got, want)
	}
}

// TestBuilderHaltReasonSanitizesControlCharacters pins the defence in depth at
// the source: firstLine cuts only on a newline, so a carriage return or an
// escape sequence sitting mid-string survives into the halt reason every view
// then draws. The reason must be inert before it leaves here.
// Mutation: return the raw v.
func TestBuilderHaltReasonSanitizesControlCharacters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tail reporttail.Tail
		note string
		want string
	}{
		{
			name: "escape in the tail",
			tail: reporttail.Tail{HaltedAt: "step 3\x1b[2J"},
			want: "halted_at: step 3\uFFFD[2J",
		},
		{
			name: "carriage return in the note",
			note: "gate=fail\rrecovered",
			want: "note: gate=failrecovered",
		},
		{
			name: "escape in the first not_done item",
			tail: reporttail.Tail{NotDone: []string{"finish the tests\x07", "then the docs"}},
			want: "not_done: finish the tests\uFFFD",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuilderHaltReason(tc.tail, tc.note, "halted")
			if got != tc.want {
				t.Fatalf("BuilderHaltReason = %q, want %q", got, tc.want)
			}
			if strings.ContainsRune(got, '\x1b') {
				t.Errorf("BuilderHaltReason = %q, want no raw escape byte", got)
			}
		})
	}
}
