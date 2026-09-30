package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

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
