package setup

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseAnswerAcceptsChoiceAndEffortTokens pins what ParseAnswer hands
// forward: a choice value verbatim, a candidate choice with a trailing
// `:high`-style reasoning effort kept on the token, and the y/n a TTY prompt
// spells out.
func TestParseAnswerAcceptsChoiceAndEffortTokens(t *testing.T) {
	builder := builderQuestion(Inventory{Harnesses: []HarnessStatus{
		status("codex", true, "gpt-6-luna"),
		status("claude", true, "sonnet", "opus", "haiku"),
	}})
	got, err := ParseAnswer(builder, "codex/openai/gpt-6-luna:high,claude/anthropic/sonnet")
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if want := []string{"codex/openai/gpt-6-luna:high", "claude/anthropic/sonnet"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %v, want %v", got, want)
	}

	tier := tierQuestion()
	got, err = ParseAnswer(tier, "yolo")
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if want := []string{"yolo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tier answer = %v, want %v", got, want)
	}

	plugin := yesNoQuestion("plugin", "p?", "y", "n")
	got, err = ParseAnswer(plugin, "y")
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if want := []string{"yes"}; !reflect.DeepEqual(got, want) {
		t.Errorf("y answer = %v, want the spelled yes", got)
	}
}

// TestParseAnswerRejectsNonChoiceNamingValidValues pins the refusal an
// invalid answer earns: it names the values that would have stood.
func TestParseAnswerRejectsNonChoiceNamingValidValues(t *testing.T) {
	q := tierQuestion()
	_, err := ParseAnswer(q, "chmod")
	if err == nil {
		t.Fatal("ParseAnswer with a non-choice: want error, got nil")
	}
	if !strings.Contains(err.Error(), "valid values: yolo, edit") {
		t.Fatalf("refusal %q does not name the valid values", err)
	}

	builder := builderQuestion(Inventory{Harnesses: []HarnessStatus{
		status("codex", true, "gpt-6-luna", "gpt-5.6-terra"),
	}})
	_, err = ParseAnswer(builder, "codex/openai/gpt-9-gone:high")
	if err == nil {
		t.Fatal("ParseAnswer with an off-list model: want error, got nil")
	}
	if !strings.Contains(err.Error(), "codex/openai/gpt-6-luna") {
		t.Fatalf("refusal %q does not name the offered choices", err)
	}
}
