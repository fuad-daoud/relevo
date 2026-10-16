package setup

import (
	"reflect"
	"testing"
)

// TestAgyEffortsFoldIntoOneChoice pins the grouping: agy's per-effort models
// are one choice listing its efforts lightest first, a lone effort stays a
// choice that asks nothing, and a model with no suffix stands as itself.
func TestAgyEffortsFoldIntoOneChoice(t *testing.T) {
	got := agyChoices([]string{
		"gemini-4-argon-medium", "gemini-4-argon-high", "gemini-4-argon-low",
		"gpt-oss-120b-medium", "gemini-3.7-pro",
	})
	want := []struct {
		label   string
		efforts []string
	}{
		{"gemini-4-argon", []string{"low", "medium", "high"}},
		{"gpt-oss-120b", []string{"medium"}},
		{"gemini-3.7-pro", nil},
	}
	if len(got) != len(want) {
		t.Fatalf("agy choices = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		if got[i].Label != w.label || !reflect.DeepEqual(got[i].Efforts, w.efforts) {
			t.Errorf("choice %d = %s %v, want %s %v", i, got[i].Label, got[i].Efforts, w.label, w.efforts)
		}
	}
}

// TestEffortAnswersSpellEachHarnessesModel pins what an answer at an effort
// becomes: agy's own model name, a claude or codex model with :effort, and
// the stored full agy name still matching its choice.
func TestEffortAnswersSpellEachHarnessesModel(t *testing.T) {
	codex := status("codex", true, "gpt-6-luna")
	codex.Efforts = map[string][]string{"gpt-6-luna": {"low", "medium", "high"}}
	inv := Inventory{Harnesses: []HarnessStatus{
		codex,
		status("agy", true, "gemini-3.8-flash-low", "gemini-3.8-flash-high", "gpt-oss-120b-medium"),
		status("claude", true, "sonnet", "haiku"),
	}}
	q := pickOf(t, Questions(inv, nil), "builder")
	for raw, want := range map[string]string{
		"agy/google/gemini-3.8-flash:low":  "agy/google/gemini-3.8-flash-low",
		"agy/google/gemini-3.8-flash-high": "agy/google/gemini-3.8-flash-high",
		"agy/google/gemini-3.8-flash":      "agy/google/gemini-3.8-flash-low",
		"agy/google/gpt-oss-120b":          "agy/google/gpt-oss-120b-medium",
		"claude/anthropic/sonnet:max":      "claude/anthropic/sonnet:max",
		"claude/anthropic/sonnet":          "claude/anthropic/sonnet",
		"codex/openai/gpt-6-luna:low":      "codex/openai/gpt-6-luna:low",
	} {
		got, err := ParseAnswer(q, raw)
		if err != nil || !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("ParseAnswer(%q) = %v, %v; want %s", raw, got, err, want)
		}
	}
	for _, bad := range []string{"claude/anthropic/haiku:high", "codex/openai/gpt-6-luna:ultra", "agy/google/gemini-3.8-flash:medium"} {
		if got, err := ParseAnswer(q, bad); err == nil {
			t.Errorf("ParseAnswer(%q) = %v, want an effort the model does not run at refused", bad, got)
		}
	}
}

// TestEffortQuestionFollowsAPickedModel pins the follow-up: asked only for a
// model with several efforts, recommending the pick's own effort, and its
// answer moving the pick to that effort.
func TestEffortQuestionFollowsAPickedModel(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{
		status("agy", true, "gemini-3.8-flash-low", "gemini-3.8-flash-high", "gpt-oss-120b-medium"),
		status("claude", true, "sonnet", "haiku"),
	}}
	q := pickOf(t, Questions(inv, nil), "builder")

	eq, ok := EffortQuestion(q, "agy/google/gemini-3.8-flash-high")
	if !ok {
		t.Fatal("no effort question for a model at two efforts")
	}
	if !reflect.DeepEqual(eq.Recommended, []string{"high"}) || len(eq.Choices) != 2 {
		t.Errorf("effort question = %+v, want low/high recommending high", eq)
	}
	if got := WithEffort(q, "agy/google/gemini-3.8-flash-high", "low"); got != "agy/google/gemini-3.8-flash-low" {
		t.Errorf("WithEffort = %s, want the low model", got)
	}

	eq, ok = EffortQuestion(q, "claude/anthropic/sonnet")
	if !ok || !reflect.DeepEqual(eq.Recommended, []string{"medium"}) {
		t.Errorf("sonnet effort question = %+v, %v; want one recommending medium", eq, ok)
	}
	for _, none := range []string{"agy/google/gpt-oss-120b-medium", "claude/anthropic/haiku"} {
		if _, ok := EffortQuestion(q, none); ok {
			t.Errorf("%s got an effort question, want none for one effort or none", none)
		}
	}
}

// TestCodexOfferCarriesReportedEfforts pins the probe side: codex's reported
// reasoning levels and default reach the status.
func TestCodexOfferCarriesReportedEfforts(t *testing.T) {
	out := []byte(`[{"slug": "gpt-6-luna", "visibility": "list", "priority": 1, "default_reasoning_level": "medium",
		"supported_reasoning_levels": [{"effort": "low"}, {"effort": "high"}]}]`)
	models, efforts, defaults := codexOffer(codexModels(out))
	if !reflect.DeepEqual(models, []string{"gpt-6-luna"}) ||
		!reflect.DeepEqual(efforts["gpt-6-luna"], []string{"low", "high"}) ||
		defaults["gpt-6-luna"] != "medium" {
		t.Errorf("codexOffer = %v %v %v", models, efforts, defaults)
	}
}

// TestDerivedReaderReusesAPickedEffort pins the dedup: the readers' sonnet is
// the builder's sonnet at the effort the builder picked, not a second
// candidate.
func TestDerivedReaderReusesAPickedEffort(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{status("claude", true, "sonnet", "opus", "haiku")}}
	files, err := Build(inv, RecommendedAnswers(inv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files.CandidateNames, []string{"sonnet", "opus"}) {
		t.Errorf("candidates = %v, want sonnet and opus once each", files.CandidateNames)
	}
}
