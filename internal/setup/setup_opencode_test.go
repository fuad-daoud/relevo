package setup

import (
	"bufio"
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// opencodeTestQuestion is the opencode question over a catalog with one
// long provider (openrouter, holding the lineup) and one short one.
func opencodeTestQuestion(t *testing.T) Question {
	t.Helper()
	catalog := []string{"cline-pass/cline-pass/glm-5.3-flash", "cline-pass/cline-pass/kimi-k3"}
	for i := 0; i < 40; i++ {
		catalog = append(catalog, fmt.Sprintf("openrouter/vendor/model-%02d", i))
	}
	catalog = append(catalog, "openrouter/qwen/qwen3.8-coder", "openrouter/qwen/qwen3.8-max",
		"openrouter/z-ai/glm-5.3-flash", "openrouter/deepseek/deepseek-v4.1-flash")
	inv := Inventory{Harnesses: []HarnessStatus{{Harness: "opencode", OnPath: true, LoggedIn: true, Catalog: catalog}}}
	for _, q := range Questions(inv, nil) {
		if q.ID == "opencode" {
			return q
		}
	}
	t.Fatal("no opencode question")
	return Question{}
}

// TestOpencodeWalkPicksByProvider pins the terminal walk: providers first,
// a short provider's whole list, a long one's shortlist, then a search whose
// matches are picked by number and a full id taken as typed.
func TestOpencodeWalkPicksByProvider(t *testing.T) {
	q := opencodeTestQuestion(t)
	// providers: 1 cline-pass, 2 openrouter -> "1,2"; cline-pass list: "2"
	// (kimi); openrouter's starting list: Enter adds none; search "qwen" ->
	// pick 2; type a full id; Enter ends the search.
	in := "1,2\n2\n\nqwen\n2\nvendor/model-07\n\n"
	out := new(bytes.Buffer)
	got, err := askOpencodeModels(out, bufio.NewScanner(strings.NewReader(in)), q)
	if err != nil {
		t.Fatalf("walk: %v\n%s", err, out)
	}
	want := []string{
		"opencode/cline-pass/cline-pass/kimi-k3",
		"opencode/openrouter/qwen/qwen3.8-max",
		"opencode/openrouter/vendor/model-07",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("picks = %v\nwant    %v\n%s", got, want, out)
	}
	for _, line := range []string{"openrouter has 44 models; here are a few to start from", "2 openrouter models hold \"qwen\""} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("walk lacks %q:\n%s", line, out)
		}
	}
	if strings.Contains(out.String(), "(recommended") {
		t.Errorf("the opencode walk recommends something:\n%s", out)
	}
	if strings.Contains(out.String(), "model-39") {
		t.Errorf("the long provider's whole list was printed:\n%s", out)
	}
}

// TestOpencodeWalkNonePicksNothing pins the skip: none answers the
// question with no model.
func TestOpencodeWalkNonePicksNothing(t *testing.T) {
	q := opencodeTestQuestion(t)
	got, err := askOpencodeModels(new(bytes.Buffer), bufio.NewScanner(strings.NewReader("none\n")), q)
	if err != nil || !slices.Equal(got, []string{OpencodeNone}) {
		t.Fatalf("none = %v, %v", got, err)
	}
}

// TestSetupFlagsReadOpencodeFirst pins the flag order rule: a builder answer
// naming an opencode model only the opencode answer picked is valid.
func TestSetupFlagsReadOpencodeFirst(t *testing.T) {
	q := opencodeTestQuestion(t)
	inv := Inventory{Harnesses: []HarnessStatus{{Harness: "opencode", OnPath: true, LoggedIn: true, Catalog: catalogOf(q)}}}
	questionsFor := func(a Answers) []Question { return Questions(inv, withOpencodeAnswer(nil, a)) }
	raw := []string{"builder=opencode/openrouter/qwen/qwen3.8-max", "opencode=opencode/openrouter/qwen/qwen3.8-max"}
	qs, err := setupQuestionsFor(raw, questionsFor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSetupAnswers(raw, qs); err != nil {
		t.Fatalf("builder on a picked opencode model refused: %v", err)
	}
	if _, err := parseSetupAnswers(raw, questionsFor(nil)); err == nil {
		t.Fatal("without the opencode answer read first the builder pick should not be offered")
	}
}

func catalogOf(q Question) []string {
	var out []string
	for _, c := range OpencodeCatalog(q) {
		out = append(out, c.Label)
	}
	return out
}
