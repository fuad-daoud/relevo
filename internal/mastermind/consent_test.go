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

	// A session with its own record is briefed even when the repo has not
	// answered: the session answered for itself.
	if got := ConsentText(ConsentUnset, &rec); !strings.HasPrefix(got, hookContext(rec)) {
		t.Errorf("unset text with a session record = %q, want the identity sentence", got)
	}
	// A `no` hides any record: the session is not briefed even when one exists.
	if got := ConsentText(ConsentNo, &rec); got != "" {
		t.Errorf("no text with a session record = %q, want empty", got)
	}

	if got := ConsentText(ConsentNo, nil); got != "" {
		t.Errorf("no text = %q, want empty", got)
	}
}

// TestEffectiveConsent pins the precedence rule: the session's own answer wins
// when it has one, otherwise the repository's.
func TestEffectiveConsent(t *testing.T) {
	cases := []struct {
		session, repo, want Consent
	}{
		{ConsentUnset, ConsentUnset, ConsentUnset},
		{ConsentUnset, ConsentYes, ConsentYes},
		{ConsentUnset, ConsentNo, ConsentNo},
		{ConsentYes, ConsentUnset, ConsentYes},
		{ConsentYes, ConsentNo, ConsentYes},
		{ConsentNo, ConsentUnset, ConsentNo},
		{ConsentNo, ConsentYes, ConsentNo},
		{ConsentYes, ConsentYes, ConsentYes},
		{ConsentNo, ConsentNo, ConsentNo},
	}
	for _, tc := range cases {
		if got := EffectiveConsent(tc.session, tc.repo); got != tc.want {
			t.Errorf("EffectiveConsent(%q, %q) = %q, want %q", tc.session, tc.repo, got, tc.want)
		}
	}
}

// TestStatusToken pins the token per effective answer and record: a record
// names a mastermind, unset is the open question, and anything else is none.
func TestStatusToken(t *testing.T) {
	rec := &Record{ID: "mm_aaaaaaaaaaaa", Name: "architect-1"}
	if got := StatusToken(ConsentYes, rec); got != "mastermind:mm_aaaaaaaaaaaa:architect-1" {
		t.Errorf("StatusToken(yes, rec) = %q", got)
	}
	if got := StatusToken(ConsentUnset, nil); got != StatusAsk {
		t.Errorf("StatusToken(unset, nil) = %q, want ask", got)
	}
	if got := StatusToken(ConsentNo, nil); got != StatusNone {
		t.Errorf("StatusToken(no, nil) = %q, want none", got)
	}
	if got := StatusToken(ConsentYes, nil); got != StatusNone {
		t.Errorf("StatusToken(yes, nil) = %q, want none until a record exists", got)
	}

	id, name, ok := StatusMasterMindParts("mastermind:mm_aaaaaaaaaaaa:architect-1")
	if !ok || id != "mm_aaaaaaaaaaaa" || name != "architect-1" {
		t.Errorf("StatusMasterMindParts = (%q, %q, %v)", id, name, ok)
	}
	for _, tok := range []string{StatusNone, StatusAsk, "", "mastermind:", "mastermind:id", "mastermind:id:"} {
		if _, _, ok := StatusMasterMindParts(tok); ok {
			t.Errorf("StatusMasterMindParts(%q) reported a mastermind token", tok)
		}
	}
}

// TestStatusNotice pins every transition the status line covers: no change is
// empty, a grant briefs, a rename is one line, a revocation says to stop, and
// an answered question says not to ask again.
func TestStatusNotice(t *testing.T) {
	rec := &Record{ID: "mm_aaaaaaaaaaaa", Name: "architect-1"}
	token := "mastermind:mm_aaaaaaaaaaaa:architect-1"

	// No change, in every pair.
	for _, tok := range []string{StatusNone, StatusAsk, token} {
		if got := StatusNotice(tok, tok, rec); got != "" {
			t.Errorf("StatusNotice(%q, %q) = %q, want empty", tok, tok, got)
		}
	}

	if got := StatusNotice(StatusAsk, token, rec); got != ConsentText(ConsentYes, rec) {
		t.Errorf("ask -> mastermind = %q, want the identity sentence and guide", got)
	}
	if got := StatusNotice(StatusNone, token, rec); got != ConsentText(ConsentYes, rec) {
		t.Errorf("none -> mastermind = %q, want the identity sentence and guide", got)
	}

	if got := StatusNotice(token, StatusNone, rec); !strings.Contains(got, "no longer a relevo MasterMind") || !strings.Contains(got, "no bind, send, wait, or done") {
		t.Errorf("mastermind -> none = %q, want the stop-acting line", got)
	}
	if got := StatusNotice(StatusAsk, StatusNone, rec); !strings.Contains(got, "answered no") || !strings.Contains(got, "Do not ask again") {
		t.Errorf("ask -> none = %q, want the answered-no line", got)
	}
	if got := StatusNotice(StatusNone, StatusAsk, rec); got != AskNote {
		t.Errorf("none -> ask = %q, want the ask-note", got)
	}

	renamed := "mastermind:mm_aaaaaaaaaaaa:reviewer-2"
	got := StatusNotice(token, renamed, &Record{ID: "mm_aaaaaaaaaaaa", Name: "reviewer-2"})
	if got == "" || strings.Contains(got, "\n") || !strings.Contains(got, "reviewer-2") {
		t.Errorf("rename notice = %q, want one line naming the new name", got)
	}

	// A different mastermind is a fresh grant, not a rename.
	other := "mastermind:mm_bbbbbbbbbbbb:reviewer-3"
	if got := StatusNotice(token, other, &Record{ID: "mm_bbbbbbbbbbbb", Name: "reviewer-3"}); got != ConsentText(ConsentYes, &Record{ID: "mm_bbbbbbbbbbbb", Name: "reviewer-3"}) {
		t.Errorf("mastermind -> another mastermind = %q, want the identity sentence and guide", got)
	}
}

// TestHookConsentForCarriesTheEvent pins that the notice's own event rides the
// envelope, so Claude Code routes it to UserPromptSubmit.
func TestHookConsentForCarriesTheEvent(t *testing.T) {
	var env hookEnvelope
	out := HookConsentFor(HookEventUserPromptSubmit, "notice text")
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("HookConsentFor is not the hook envelope: %v", err)
	}
	if env.HookSpecificOutput.HookEventName != HookEventUserPromptSubmit {
		t.Errorf("hookEventName = %q, want %q", env.HookSpecificOutput.HookEventName, HookEventUserPromptSubmit)
	}
	if env.HookSpecificOutput.AdditionalContext != "notice text" {
		t.Errorf("additionalContext = %q, want the text", env.HookSpecificOutput.AdditionalContext)
	}
	if got := string(HookConsentFor(HookEventUserPromptSubmit, "")); got != "{}\n" {
		t.Errorf("HookConsentFor(empty) = %q, want {}\\n", got)
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
