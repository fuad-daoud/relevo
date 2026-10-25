package setup

import (
	"reflect"
	"testing"
)

// pickOf fetches one question by id.
func pickOf(t *testing.T, qs []Question, id string) Question {
	t.Helper()
	for _, q := range qs {
		if q.ID == id {
			return q
		}
	}
	t.Fatalf("no %s question in %v", id, ids(qs))
	return Question{}
}

func ids(qs []Question) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}

// recommendedOf reads one question's recommended values.
func recommendedOf(t *testing.T, q Question) []string {
	t.Helper()
	if len(q.Recommended) == 0 {
		t.Fatalf("%s recommends nothing: %v", q.ID, q.Recommended)
	}
	return q.Recommended
}

// TestQuestionsRecommendOneModelPerLoggedInHarness pins the builder
// recommendation: one model per logged-in harness at medium effort, codex's
// first listed model, agy's first flash model (not simply its first), and
// claude sonnet -- with agy's efforts folded into one choice per model.
func TestQuestionsRecommendOneModelPerLoggedInHarness(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{
		status("codex", true, "gpt-6-luna", "gpt-5.6-terra"),
		status("agy", true, "gemini-3.7-pro", "gemini-3.8-flash-high"),
		status("claude", true, "sonnet", "opus", "haiku"),
	}}
	q := pickOf(t, Questions(inv, nil), "builder")
	want := []string{
		"codex/openai/gpt-6-luna:medium",
		"agy/google/gemini-3.8-flash-high",
		"claude/anthropic/sonnet:medium",
	}
	if got := recommendedOf(t, q); !reflect.DeepEqual(got, want) {
		t.Errorf("builder recommends %v, want %v", got, want)
	}
	if !q.Multi {
		t.Error("builder question = single pick, want an ordered multi")
	}
	var wantValues []string
	for _, c := range q.Choices {
		wantValues = append(wantValues, c.Value)
	}
	if want := []string{
		"codex/openai/gpt-6-luna", "codex/openai/gpt-5.6-terra",
		"agy/google/gemini-3.7-pro", "agy/google/gemini-3.8-flash",
		"claude/anthropic/sonnet", "claude/anthropic/opus", "claude/anthropic/haiku",
	}; !reflect.DeepEqual(wantValues, want) {
		t.Errorf("builder choices = %v, want %v", wantValues, want)
	}
}

// TestQuestionsPlannerFallsBackWithoutClaude pins the planner
// recommendation without claude: codex's first listed model at high, then
// agy's first claude-opus-* model else its first.
func TestQuestionsPlannerFallsBackWithoutClaude(t *testing.T) {
	for _, tt := range []struct {
		kind string
		inv  Inventory
		want []string
	}{
		{"codex first at high", Inventory{Harnesses: []HarnessStatus{
			status("codex", true, "gpt-6-luna", "gpt-5.6-terra"),
		}}, []string{"codex/openai/gpt-6-luna:high"}},
		{"agy claude-opus model", Inventory{Harnesses: []HarnessStatus{
			status("agy", true, "gemini-3.7-pro", "claude-opus-4-8"),
		}}, []string{"agy/google/claude-opus-4-8"}},
		{"agy first model", Inventory{Harnesses: []HarnessStatus{
			status("agy", true, "gemini-3.9-pro", "gemini-3.8-flash-high"),
		}}, []string{"agy/google/gemini-3.9-pro"}},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			q := pickOf(t, Questions(tt.inv, nil), "planner")
			if got := recommendedOf(t, q); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("planner recommends %v, want %v", got, tt.want)
			}
		})
	}
}

// TestQuestionsDropsPluginWhenInstalledOrClaudeAbsent pins the plugin
// question's own rules: asked only when claude stands on PATH and no answer
// for it already does -- an installer-recorded answer stands in for the
// installed state.
func TestQuestionsDropsPluginWhenInstalledOrClaudeAbsent(t *testing.T) {
	claudeInv := Inventory{Harnesses: []HarnessStatus{
		status("claude", true, "sonnet", "opus", "haiku"),
	}}
	if _, ok := findID(Questions(claudeInv, nil), "plugin"); !ok {
		t.Fatal("plugin question missing with claude on PATH and none answered")
	}
	if _, ok := findID(Questions(claudeInv, map[string][]string{"plugin": {"yes"}}), "plugin"); ok {
		t.Error("plugin question kept while an answer already stands for it")
	}
	if got := ids(Questions(Inventory{Harnesses: []HarnessStatus{status("codex", true, "gpt-6-luna")}}, nil)); contains(got, "plugin") {
		t.Errorf("questions %v carry plugin with no claude on PATH, want it dropped", got)
	}
}

// TestQuestionsDropsMusePluginWhenAnsweredOrMuseAbsent mirrors the plugin
// question's rules for muse-plugin: asked only when muse stands on PATH and
// no answer for it already does -- a stored answer stands in for the fact.
func TestQuestionsDropsMusePluginWhenAnsweredOrMuseAbsent(t *testing.T) {
	museInv := Inventory{Harnesses: []HarnessStatus{
		status("muse", true, "gemini-3.8-flash"),
	}}
	if _, ok := findID(Questions(museInv, nil), "muse-plugin"); !ok {
		t.Fatal("muse-plugin question missing with muse on PATH and none answered")
	}
	if _, ok := findID(Questions(museInv, map[string][]string{"muse-plugin": {"yes"}}), "muse-plugin"); ok {
		t.Error("muse-plugin question kept while an answer already stands for it")
	}
	if got := ids(Questions(Inventory{Harnesses: []HarnessStatus{status("codex", true, "gpt-6-luna")}}, nil)); contains(got, "muse-plugin") {
		t.Errorf("questions %v carry muse-plugin with no muse on PATH, want it dropped", got)
	}
}

// TestQuestionsPluginAndMusePluginAreIndependent pins the two questions'
// independence: with both binaries on PATH both are asked, each gated only
// on its own binary and not on the other's answer.
func TestQuestionsPluginAndMusePluginAreIndependent(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{
		status("claude", true, "sonnet", "opus", "haiku"),
		status("muse", true, "gemini-3.8-flash"),
	}}
	qs := Questions(inv, nil)
	if _, ok := findID(qs, "plugin"); !ok {
		t.Errorf("questions %v lack plugin with claude on PATH", ids(qs))
	}
	if _, ok := findID(qs, "muse-plugin"); !ok {
		t.Errorf("questions %v lack muse-plugin with muse on PATH", ids(qs))
	}
	qs = Questions(inv, map[string][]string{"plugin": {"yes"}})
	if _, ok := findID(qs, "muse-plugin"); !ok {
		t.Errorf("questions %v lack muse-plugin while plugin is answered, want it independent", ids(qs))
	}
}

// TestQuestionsDropsServiceWhenUnitInstalled pins the service question's
// drop rule: no unit install question while the caller knows a unit already
// runs.
func TestQuestionsDropsServiceWhenUnitInstalled(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{status("codex", true, "gpt-6-luna")}}
	if _, ok := findID(Questions(inv, nil), "service"); !ok {
		t.Fatal("service question missing when nothing answered yet")
	}
	if _, ok := findID(Questions(inv, map[string][]string{"service": {"yes"}}), "service"); ok {
		t.Error("service question kept while the unit already runs")
	}
}

// TestRecommendedAnswersKeepCurrentConfig pins the rerun rule: a stored
// config's own values are what the recommendations carry while they are
// still among the choices, and a value the machine no longer offers falls
// back to the defaults.
func TestRecommendedAnswersKeepCurrentConfig(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{
		status("codex", true, "gpt-6-luna", "gpt-5.6-terra"),
		status("agy", true, "gemini-3.8-flash-high"),
	}}
	current := map[string][]string{
		"builder": {"agy/google/gemini-3.8-flash-high", "codex/openai/gpt-5.6-terra:high"},
		"planner": {"codex/openai/gpt-6-luna:high"},
		"tier":    {"edit"},
	}
	rec := RecommendedAnswers(inv, current)
	for id, want := range map[string][]string{
		"builder": {"agy/google/gemini-3.8-flash-high", "codex/openai/gpt-5.6-terra:high"},
		"planner": {"codex/openai/gpt-6-luna:high"},
		"tier":    {"edit"},
	} {
		if got := rec[id]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s recommends %v, want the stored %v", id, got, want)
		}
	}

	current["builder"] = []string{"codex/openai/gpt-9-gone:high"}
	rec = RecommendedAnswers(inv, current)
	fallback := []string{"codex/openai/gpt-6-luna:medium", "agy/google/gemini-3.8-flash-high"}
	if got := rec["builder"]; !reflect.DeepEqual(got, fallback) {
		t.Errorf("builder recommends %v with only stale values stored, want the defaults %v", got, fallback)
	}
}

// findID reports whether one id stands in a question list.
func findID(qs []Question, id string) (Question, bool) {
	for _, q := range qs {
		if q.ID == id {
			return q, true
		}
	}
	return Question{}, false
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestQuestionsListMuseModelsAndRecommendThem pins the wizard over a
// logged-in muse probed through the fake: the builder lists the catalog at
// muse/meta values in default order with the efforts on the choice, and
// recommends the first model at medium; with muse alone the planner falls
// back to that model at high and the role questions carry the same choices,
// the reviewer and the lite-planner derived from the first muse model at
// medium. With codex logged in too, muse's models follow codex's in the
// list and the builder recommends both.
func TestQuestionsListMuseModelsAndRecommendThem(t *testing.T) {
	inv := Probe(probeEnv{pathEnv: pathEnv{
		onPath: map[string]bool{"muse": true},
		files:  map[string][]byte{".config/muse/auth.json": []byte("{}")},
	}})
	qs := Questions(inv, nil)
	builder := pickOf(t, qs, "builder")
	wantValues := []string{"muse/meta/muse-spark-1.3", "muse/meta/muse-spark-1.2", "muse/meta/muse-wonder-1.0"}
	var values []string
	for _, c := range builder.Choices {
		values = append(values, c.Value)
	}
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatalf("builder choices = %v, want the muse catalog at muse/meta values %v", values, wantValues)
	}
	wantEfforts := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	if !reflect.DeepEqual(builder.Choices[0].Efforts, wantEfforts) {
		t.Errorf("muse choice efforts = %v, want %v", builder.Choices[0].Efforts, wantEfforts)
	}
	if got := recommendedOf(t, builder); !reflect.DeepEqual(got, []string{"muse/meta/muse-spark-1.3:medium"}) {
		t.Errorf("builder recommends %v, want the first muse model at medium", got)
	}
	if got := recommendedOf(t, pickOf(t, qs, "planner")); !reflect.DeepEqual(got, []string{"muse/meta/muse-spark-1.3:high"}) {
		t.Errorf("planner recommends %v, want the first muse model at high", got)
	}
	for _, id := range []string{"reviewer", "lite-planner"} {
		if got := recommendedOf(t, pickOf(t, qs, id)); !reflect.DeepEqual(got, []string{"muse/meta/muse-spark-1.3:medium"}) {
			t.Errorf("%s recommends %v, want the cheapest muse pick at medium", id, got)
		}
	}
	if got := recommendedOf(t, pickOf(t, qs, "security")); !reflect.DeepEqual(got, []string{"muse/meta/muse-spark-1.3:high"}) {
		t.Errorf("security recommends %v, want the planner's muse pick", got)
	}

	both := Probe(probeEnv{
		pathEnv: pathEnv{
			onPath: map[string]bool{"codex": true, "muse": true},
			files:  map[string][]byte{".config/muse/auth.json": []byte("{}")},
		},
		run: func(name string, args ...string) ([]byte, int, error) {
			switch callKey(name, args...) {
			case "codex login status":
				return nil, 0, nil
			case "codex debug models":
				return []byte(`[{"slug": "gpt-6-luna", "visibility": "list", "priority": 1}]`), 0, nil
			}
			return nil, -1, errProbeTimeout
		},
	})
	q := pickOf(t, Questions(both, nil), "builder")
	var bothValues []string
	for _, c := range q.Choices {
		bothValues = append(bothValues, c.Value)
	}
	wantBoth := []string{"codex/openai/gpt-6-luna", "muse/meta/muse-spark-1.3", "muse/meta/muse-spark-1.2", "muse/meta/muse-wonder-1.0"}
	if !reflect.DeepEqual(bothValues, wantBoth) {
		t.Errorf("builder choices with codex too = %v, want codex then muse %v", bothValues, wantBoth)
	}
	if got := recommendedOf(t, q); !reflect.DeepEqual(got, []string{"codex/openai/gpt-6-luna:medium", "muse/meta/muse-spark-1.3:medium"}) {
		t.Errorf("builder recommends %v with codex and muse, want both first models at medium", got)
	}
}
