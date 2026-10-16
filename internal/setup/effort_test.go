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
	for _, bad := range []string{"claude/anthropic/haiku:ultra", "codex/openai/gpt-6-luna:ultra", "agy/google/gemini-3.8-flash:medium"} {
		if got, err := ParseAnswer(q, bad); err == nil {
			t.Errorf("ParseAnswer(%q) = %v, want an effort the model does not run at refused", bad, got)
		}
	}
}

// TestEffortQuestionFollowsAPickedModel pins the follow-up: asked only for a
// model with several efforts, recommending the pick's own effort else the
// builder's medium, and its answer moving the pick to that effort.
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
	// opus is not a recommended builder, so it takes the builder's own
	// medium rather than the planner's high.
	q = pickOf(t, Questions(Inventory{Harnesses: []HarnessStatus{status("claude", true, "sonnet", "opus")}}, nil), "builder")
	if eq, ok := EffortQuestion(q, "claude/anthropic/opus"); !ok || !reflect.DeepEqual(eq.Recommended, []string{"medium"}) {
		t.Errorf("opus builder effort = %v, %v; want medium", eq.Recommended, ok)
	}
	q = pickOf(t, Questions(inv, nil), "builder")
	for _, none := range []string{"agy/google/gpt-oss-120b-medium", "agy/google/gemini-3.7-pro"} {
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

// TestPlannerRecommendsHighEffort pins the planner's effort: a planner model
// picked by number, carrying no effort, is recommended high -- not codex's
// own medium default.
func TestPlannerRecommendsHighEffort(t *testing.T) {
	codex := status("codex", true, "gpt-6-luna", "gpt-5.6-terra")
	codex.Efforts = map[string][]string{"gpt-6-luna": {"low", "medium", "high"}, "gpt-5.6-terra": {"low", "medium", "high"}}
	codex.DefaultEffort = map[string]string{"gpt-6-luna": "medium", "gpt-5.6-terra": "medium"}
	q := pickOf(t, Questions(Inventory{Harnesses: []HarnessStatus{codex}}, nil), "planner")
	// gpt-6-luna is the recommended planner; gpt-5.6-terra is not, so it
	// takes the high fallback rather than the recommendation's effort.
	for _, picked := range []string{"codex/openai/gpt-6-luna", "codex/openai/gpt-5.6-terra"} {
		eq, ok := EffortQuestion(q, picked)
		if !ok || !reflect.DeepEqual(eq.Recommended, []string{"high"}) {
			t.Errorf("%s effort question = %+v, %v; want high recommended", picked, eq.Recommended, ok)
		}
	}
	claude := pickOf(t, Questions(Inventory{Harnesses: []HarnessStatus{status("claude", true, "sonnet", "opus")}}, nil), "planner")
	if !reflect.DeepEqual(claude.Recommended, []string{"claude/anthropic/opus:high"}) {
		t.Errorf("claude planner recommends %v, want opus at high", claude.Recommended)
	}
}

// TestRolesDeriveUnlessAnswered pins the optional roles: left unanswered,
// the librarian follows the builders, security the planner, and the
// reviewer and lite-planner the cheapest model; answered, each role writes
// its own pick.
func TestRolesDeriveUnlessAnswered(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{status("claude", true, "sonnet", "opus", "haiku")}}
	answers := Answers{
		"builder": {"claude/anthropic/haiku:low"},
		"planner": {"claude/anthropic/opus:high"},
		"tier":    {"yolo"},
	}
	files, err := Build(inv, answers)
	if err != nil {
		t.Fatal(err)
	}
	actors := parseActorsOf(t, files)
	for role, want := range map[string][]string{
		"librarian": {"haiku"}, "security": {"opus"}, "reviewer": {"sonnet"}, "lite-planner": {"sonnet"},
	} {
		if got := actorNames(actors[role]); !reflect.DeepEqual(got, want) {
			t.Errorf("derived %s = %v, want %v", role, got, want)
		}
	}

	answers["reviewer"] = []string{"claude/anthropic/opus:high"}
	answers["librarian"] = []string{"claude/anthropic/sonnet:low"}
	if files, err = Build(inv, answers); err != nil {
		t.Fatal(err)
	}
	actors = parseActorsOf(t, files)
	if got := actorNames(actors["reviewer"]); !reflect.DeepEqual(got, []string{"opus"}) {
		t.Errorf("answered reviewer = %v, want opus", got)
	}
	if got := actorNames(actors["librarian"]); len(got) != 1 || got[0] == "haiku" {
		t.Errorf("answered librarian = %v, want the sonnet pick", got)
	}
	if _, ok := RecommendedAnswers(inv, nil)["reviewer"]; ok {
		t.Error("RecommendedAnswers answers a role, want it left for Build to derive")
	}
}

// TestDerivedReaderTakesThePickedEffort pins that a reviewer derived onto a
// model the builders already run takes the builders' effort for it, not the
// medium a fresh derived pick gets.
func TestDerivedReaderTakesThePickedEffort(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{status("claude", true, "sonnet", "opus", "haiku")}}
	files, err := Build(inv, Answers{"builder": {"claude/anthropic/sonnet:low"}, "planner": {"claude/anthropic/opus:high"}, "tier": {"yolo"}})
	if err != nil {
		t.Fatal(err)
	}
	var models []string
	for _, c := range candidatesOf(t, files) {
		models = append(models, c.Model)
	}
	if !reflect.DeepEqual(models, []string{"sonnet:low", "opus:high"}) {
		t.Errorf("candidates = %v, want sonnet once, at the builder's low", models)
	}
}
