package setup

import (
	"reflect"
	"slices"
	"testing"
)

// opencodeInventory is opencode with a measured-shape catalog: an openrouter
// holding the lineup and more, a small provider, and the free models.
func opencodeInventory() Inventory {
	catalog := []string{
		"cline-pass/cline-pass/glm-5.3-flash",
		"opencode/big-pickle",
		"openrouter/qwen/qwen3.8-coder",
		"openrouter/deepseek/deepseek-v4.1-flash",
		"openrouter/xiaomi/mimo-v2.6-flash",
		"openrouter/z-ai/glm-5.3-flash",
	}
	return Inventory{Harnesses: []HarnessStatus{{
		Harness: "opencode", OnPath: true, LoggedIn: true,
		Catalog: catalog, Models: defaultOpencodePicks(catalog),
	}}}
}

// TestProbeOpencodeReadsTheModelList pins the probe: one "provider/model"
// per line, and a failed list falls back to the lineup.
func TestProbeOpencodeReadsTheModelList(t *testing.T) {
	env := probeEnv{
		pathEnv: pathEnv{onPath: map[string]bool{"opencode": true}},
		run: func(string, ...string) ([]byte, int, error) {
			return []byte("opencode/big-pickle\nopenrouter/z-ai/glm-5.3-flash\n\n"), 0, nil
		},
	}
	s := Probe(env).Harnesses[0]
	if !reflect.DeepEqual(s.Catalog, []string{"opencode/big-pickle", "openrouter/z-ai/glm-5.3-flash"}) {
		t.Errorf("catalog = %v", s.Catalog)
	}
	if !reflect.DeepEqual(s.Models, []string{"openrouter/z-ai/glm-5.3-flash"}) {
		t.Errorf("default picks = %v, want the lineup model the catalog has", s.Models)
	}
}

// TestOpencodeQuestionAcceptsTheWholeCatalog pins the opencode question: it
// lists the lineup, accepts an unlisted catalog model, refuses one opencode
// cannot run, and none picks nothing.
func TestOpencodeQuestionAcceptsTheWholeCatalog(t *testing.T) {
	q := pickOf(t, Questions(opencodeInventory(), nil), "opencode")
	want := []string{"opencode/openrouter/z-ai/glm-5.3-flash", "opencode/openrouter/deepseek/deepseek-v4.1-flash", "opencode/openrouter/xiaomi/mimo-v2.6-flash"}
	if !reflect.DeepEqual(q.Recommended, want) {
		t.Errorf("recommended = %v, want the lineup", q.Recommended)
	}
	if got, err := ParseAnswer(q, "opencode/openrouter/qwen/qwen3.8-coder"); err != nil || got[0] != "opencode/openrouter/qwen/qwen3.8-coder" {
		t.Errorf("unlisted catalog model = %v, %v; want accepted", got, err)
	}
	if _, err := ParseAnswer(q, "opencode/openrouter/made/up"); err == nil {
		t.Error("a model outside the catalog was accepted")
	}
	if len(OpencodeCatalog(q)) != 6 {
		t.Errorf("catalog = %d choices, want all 6", len(OpencodeCatalog(q)))
	}
}

// TestOpencodePicksBecomeTheOtherQuestionsChoices pins the narrowing: the
// models the opencode answer picked are the opencode choices every other
// question lists, and each is a candidate; none leaves opencode out.
func TestOpencodePicksBecomeTheOtherQuestionsChoices(t *testing.T) {
	inv := opencodeInventory()
	answers := Answers{"opencode": {"opencode/openrouter/qwen/qwen3.8-coder", "opencode/cline-pass/cline-pass/glm-5.3-flash"}}
	builder := pickOf(t, Questions(inv, answers), "builder")
	var values []string
	for _, c := range builder.Choices {
		values = append(values, c.Value)
	}
	if !reflect.DeepEqual(values, answers["opencode"]) {
		t.Errorf("builder choices = %v, want the picks %v", values, answers["opencode"])
	}

	answers["builder"] = []string{"opencode/openrouter/qwen/qwen3.8-coder"}
	answers["planner"] = []string{"opencode/openrouter/qwen/qwen3.8-coder"}
	answers["tier"] = []string{"yolo"}
	files, err := Build(inv, answers)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, c := range candidatesOf(t, files) {
		refs = append(refs, c.Ref().String())
	}
	if !slices.Contains(refs, "opencode/cline-pass/cline-pass/glm-5.3-flash") {
		t.Errorf("candidates = %v, want the picked glm written though no actor runs it", refs)
	}

	answers = Answers{"opencode": {OpencodeNone}}
	if b := pickOf(t, Questions(inv, answers), "builder"); len(b.Choices) != 0 {
		t.Errorf("none still offers %v", b.Choices)
	}
}
