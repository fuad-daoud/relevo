package mastermind

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestConsentTextStates pins the four rendered answers: yes with and without a
// record, unset, and no.
func TestConsentTextStates(t *testing.T) {
	rec := Record{ID: "mm_aaaaaaaaaaaa", Name: "architect-1"}

	enabled := ConsentText(ConsentYes, &rec)
	if !strings.HasPrefix(enabled, hookContext(rec)) {
		t.Errorf("enabled text %q does not start with the identity sentence", enabled)
	}
	if !strings.HasSuffix(enabled, Guide()) {
		t.Error("enabled text does not end with the guide")
	}

	if got := ConsentText(ConsentYes, nil); got != Guide() {
		t.Errorf("enabled text without a record = %q, want the guide alone", got)
	}

	if got := ConsentText(ConsentUnset, nil); got != AskNote {
		t.Errorf("unset text = %q, want the ask-note", got)
	}

	if got := ConsentText(ConsentNo, &rec); got != "" {
		t.Errorf("no text = %q, want empty", got)
	}
}

// TestAskNoteNamesTheCommands pins the three commands the ask-note hands the
// model, so a CLI rename cannot leave the note naming a verb that is gone.
func TestAskNoteNamesTheCommands(t *testing.T) {
	for _, want := range []string{
		"relevo mastermind enable",
		"relevo mastermind enable --repo",
		"relevo mastermind disable --repo",
	} {
		if !strings.Contains(AskNote, want) {
			t.Errorf("AskNote does not name %q", want)
		}
	}
}

// TestHookConsentEmptyIsAnEmptyEnvelope pins that a repo answering no adds no
// context: the answer is a JSON object, not an envelope with empty text.
func TestHookConsentEmptyIsAnEmptyEnvelope(t *testing.T) {
	if got := string(HookConsent("")); got != "{}\n" {
		t.Errorf("HookConsent(\"\") = %q, want {}\\n", got)
	}

	var env hookEnvelope
	if err := json.Unmarshal(HookConsent("hello"), &env); err != nil {
		t.Fatalf("HookConsent is not the hook envelope: %v", err)
	}
	if got := env.HookSpecificOutput.AdditionalContext; got != "hello" {
		t.Errorf("additionalContext = %q, want hello", got)
	}
}
