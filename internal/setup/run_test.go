package setup

// setup_test.go pins the setup front end's pure rules: both documents, the
// ask-mode rule, the answer parsing, the step list, and the smoke plan's own
// words. Every fake is a fake inventory or a fake writer: no harness runs and
// no network is reached (CI has neither).

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// setupCodexInventory is the fake probe a doc or answer test builds from:
// one wizard harness logged in with two models offered, nothing else.
func setupCodexInventory() Inventory {
	return Inventory{
		Harnesses: []HarnessStatus{
			{
				Harness:  "codex",
				OnPath:   true,
				LoggedIn: true,
				Models:   []string{"gpt-6-luna", "gpt-5.6-terra"},
				Login:    "codex login",
			},
		},
	}
}

// setupUnmarshal turns printed JSON back into the generic shape the shape
// assertions walk.
func setupUnmarshal(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	return doc
}

// setupKeyList joins a generic object's keys, for an exact-key-set assert.
func setupKeyList(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// printQuestionsTo collects the questions document a print would write: the
// shape asserts below read it back, and the exit stays the questions one.
func printQuestionsTo(t *testing.T, doc SetupQuestionsDoc) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	err := setupPrintQuestions(func(v any) error { return json.NewEncoder(&buf).Encode(v) }, doc)
	return buf.Bytes(), err
}

// TestSetupQuestionsDoc pins the questions document's shape: the inventory
// rows carry the probe's facts, no login where logged in, no models where
// the CLI offered none, the questions their five-field shape with one
// three-field choice, and the next line the exact command. It also pins the
// exit mechanism: the document through the printer and the questions-open
// return.
func TestSetupQuestionsDoc(t *testing.T) {
	inv := setupCodexInventory()
	doc := setupQuestionsDocOf(inv, Questions(inv, nil))

	stdout, printErr := printQuestionsTo(t, doc)
	if !errors.Is(printErr, ErrQuestionsOpen) {
		t.Fatalf("setupPrintQuestions = %v (%T), want the questions-open return", printErr, printErr)
	}
	if got, want := setupUnmarshal(t, stdout)["next"], "relevo setup --answer builder=<...> --answer planner=<...> --answer tier=<...> --answer service=<yes|no> --answer smoke=<yes|no>"; got != want {
		t.Fatalf("next = %v, want %q", got, want)
	}
	got := setupUnmarshal(t, stdout)
	if keys := setupKeyList(got); !slices.Equal(keys, []string{"inventory", "next", "questions"}) {
		t.Fatalf("document keys = %v, want inventory/next/questions", keys)
	}
	entries := got["inventory"].([]any)
	if len(entries) != 1 {
		t.Fatalf("inventory rows = %d, want 1", len(entries))
	}
	row := entries[0].(map[string]any)
	if !slices.Equal(setupKeyList(row), []string{"harness", "logged_in", "models", "on_path"}) {
		t.Fatalf("inventory row keys = %v, want harness/logged_in/models/on_path", setupKeyList(row))
	}
	if row["harness"] != "codex" || row["on_path"] != true || row["logged_in"] != true {
		t.Fatalf("inventory row = %v, want a logged-in codex", row)
	}
	models, _ := row["models"].([]any)
	if len(models) != 2 || models[0] != "gpt-6-luna" {
		t.Fatalf("models = %v, want the probe's order", models)
	}

	qs := got["questions"].([]any)
	ids := []string{"builder", "planner", "librarian", "reviewer", "security", "lite-planner", "tier", "service", "smoke"}
	if len(qs) != len(ids) {
		t.Fatalf("questions = %d, want %d", len(qs), len(ids))
	}
	for i, qAny := range qs {
		q := qAny.(map[string]any)
		want := []string{"choices", "id", "multi", "prompt", "recommended"}
		if IsRole(ids[i]) {
			want = []string{"choices", "id", "multi", "optional", "prompt", "recommended"}
		}
		if !slices.Equal(setupKeyList(q), want) {
			t.Fatalf("%s keys = %v, want %v", ids[i], setupKeyList(q), want)
		}
		if q["id"] != ids[i] {
			t.Fatalf("question %d id = %v, want %s", i, q["id"], ids[i])
		}
		choices := q["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c := choices[0].(map[string]any)
		if !slices.Equal(setupKeyList(c), []string{"label", "value", "why"}) {
			t.Fatalf("%s choice keys = %v, want label/value/why", ids[i], setupKeyList(c))
		}
	}
}

// TestSetupQuestionsDocKeepsNotes pins that a probe note rides in the
// document beside the inventory, so --json keeps every machine fact; the
// print exit is the questions one like any other open questions run.
func TestSetupQuestionsDocKeepsNotes(t *testing.T) {
	inv := setupCodexInventory()
	inv.Notes = []string{"opencode: not covered by setup yet; configure it with relevo config"}
	stdout, err := printQuestionsTo(t, setupQuestionsDocOf(inv, Questions(inv, nil)))
	if !errors.Is(err, ErrQuestionsOpen) {
		t.Fatalf("setupPrintQuestions = %v (%T), want the questions exit", err, err)
	}
	if !strings.Contains(string(stdout), `opencode: not covered by setup yet`) {
		t.Fatalf("document = %s, want the probe note", stdout)
	}
}

// TestSetupQuestionsDocTTYRule pins the ask-mode rule: a terminal on both
// sides and no --json prompts; anything else prints the document, including
// TTY+--json, and then the document path is the printed one.
func TestSetupQuestionsDocTTYRule(t *testing.T) {
	if setupAskMode(true, true, true) {
		t.Fatal("TTY+--json must print the questions document, not prompt")
	}
	if setupAskMode(false, true, false) {
		t.Fatal("a pipe on stdin must print the questions document, not prompt")
	}
	if setupAskMode(true, false, false) {
		t.Fatal("a redirected stdout must print the questions document")
	}
	if !setupAskMode(true, true, false) {
		t.Fatal("a plain TTY run must prompt")
	}

	// The document path prints the questions document: the pure builder and
	// printer touch no state root, so the doc-only run leaves nothing behind.
	stdout, _ := printQuestionsTo(t, setupQuestionsDocOf(setupCodexInventory(), Questions(setupCodexInventory(), nil)))
	if !strings.Contains(strings.TrimSpace(string(stdout)), `"questions"`) {
		t.Fatalf("non-TTY path printed %q, want the questions document", stdout)
	}
}

// TestSetupAnswerValidationNamesChoices pins the --answer value rules: a
// value no choice names is refused naming the valid ones, and a flag for a
// question the standing facts dropped is ignored with a stderr note.
func TestSetupAnswerValidationNamesChoices(t *testing.T) {
	inv := setupCodexInventory()

	t.Run("bad value names the valid values", func(t *testing.T) {
		_, err := parseSetupAnswers([]string{"tier=maybe"}, Questions(inv, nil))
		if err == nil {
			t.Fatal("tier=maybe parsed, want a refusal")
		}
		for _, want := range []string{"invalid tier value", `"maybe"`, "yolo, edit"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name %q", err.Error(), want)
			}
		}
	})

	t.Run("bad candidate value names the chooseable models", func(t *testing.T) {
		_, err := parseSetupAnswers([]string{"builder=codex/openai/nope"}, Questions(inv, nil))
		if err == nil {
			t.Fatal("bad builder ref parsed, want a refusal")
		}
		if !strings.Contains(err.Error(), "valid values:") {
			t.Errorf("error = %q, want it to name the valid values", err.Error())
		}
	})

	t.Run("a dropped question's answer is ignored with a note", func(t *testing.T) {
		stood := Answers{"service": []string{"yes"}}
		qs := Questions(inv, stood)

		buf := new(bytes.Buffer)
		setupAnswerNotes = buf
		t.Cleanup(func() { setupAnswerNotes = new(bytes.Buffer) })

		given, err := parseSetupAnswers([]string{"service=no", "tier=yolo"}, qs)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if had := given["service"]; len(had) != 0 {
			t.Fatalf("answer for the dropped service question survived: %v", had)
		}
		if got := given["tier"]; !slices.Equal(got, []string{"yolo"}) {
			t.Fatalf("tier = %v, want yolo", got)
		}
		if !strings.Contains(buf.String(), "service") {
			t.Fatalf("note printed %q, want it to name service", buf.String())
		}
	})
}

// TestSetupResultDoc pins the result document: one row per step, the three
// fact fields, and the failed row carrying its own next.
func TestSetupResultDoc(t *testing.T) {
	steps := []SetupStepDoc{
		{Step: "config", OK: true, Detail: "builder: gpt-6-luna-high; planner: gpt-6-luna-high; tier: yolo"},
		{Step: "agents", OK: true},
		{Step: "smoke", OK: false, Detail: "round closed without a report", Next: "relevo show setup-smoke --round 1 --transcript"},
	}
	raw, err := json.Marshal(setupResultDocOf(steps))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := setupUnmarshal(t, raw)["steps"].([]any)
	if len(got) != len(steps) {
		t.Fatalf("rows = %d, want %d", len(got), len(steps))
	}
	if keys := setupKeyList(got[0].(map[string]any)); !slices.Equal(keys, []string{"detail", "ok", "step"}) {
		t.Fatalf("success keys = %v, want detail/ok/step", keys)
	}
	if keys := setupKeyList(got[1].(map[string]any)); !slices.Equal(keys, []string{"ok", "step"}) {
		t.Fatalf("agents keys = %v, want ok/step", keys)
	}
	if keys := setupKeyList(got[2].(map[string]any)); !slices.Equal(keys, []string{"detail", "next", "ok", "step"}) {
		t.Fatalf("failure keys = %v, want detail/next/ok/step", keys)
	}
	if got[2].(map[string]any)["ok"] != false {
		t.Fatal("failed smoke row reads ok")
	}
	if got[2].(map[string]any)["next"] != steps[2].Next {
		t.Fatalf("failed row next = %v, want %q", got[2].(map[string]any)["next"], steps[2].Next)
	}

	// Earlier steps stay applied and on screen: the document keeps every row
	// that ran before the failure, in apply order.
	if !got[0].(map[string]any)["ok"].(bool) || got[0].(map[string]any)["step"] != "config" {
		t.Fatalf("rows = %v, want config intact before the failure", got)
	}
}

// TestSetupStepDocOfNamesTheCode pins how a shaped step failure renders:
// the step's message and the next it carried, not the prose error alone.
// The limit failover and the not-available refusal render the same way, and
// an unshaped failure keeps only its first line.
func TestSetupStepDocOfNamesTheCode(t *testing.T) {
	doc := setupStepDocOf("service", &StepError{Message: "an edited unit", Next: "FIXCMD"})
	if doc.Step != "service" || doc.OK {
		t.Fatalf("doc = %+v, want a failed service row", doc)
	}
	if doc.Detail != "an edited unit" {
		t.Errorf("detail = %q, want the step message", doc.Detail)
	}
	if doc.Next != "FIXCMD" {
		t.Errorf("next = %q, want the step next", doc.Next)
	}

	limit := setupStepDocOf("smoke", &LimitError{Message: "gated", Next: "relevo setup"})
	if limit.Detail != "gated" || limit.Next != "relevo setup" {
		t.Errorf("limit doc = %+v, want the failover message and fix", limit)
	}

	na := setupStepDocOf("config", &NotAvailableError{Message: "no harness is logged in"})
	if na.Detail != "no harness is logged in" || na.Next != "relevo setup" {
		t.Errorf("not-available doc = %+v, want the refusal message and fix", na)
	}

	plain := setupStepDocOf("agents", errors.New("boom\nsecond line"))
	if plain.Detail != "boom" || plain.Next != "" {
		t.Errorf("plain doc = %+v, want the first line and no next", plain)
	}
}

// TestSetupPluginStepSkipsWhenNo pins the step list: a no on the plugin
// question leaves the plugin step out of the apply order.
func TestSetupPluginStepSkipsWhenNo(t *testing.T) {
	answers := Answers{"plugin": []string{"no"}, "service": []string{"yes"}, "smoke": []string{"yes"}}
	if got := setupStepsFor(answers); slices.Contains(got, "plugin") {
		t.Fatalf("steps = %v, want no plugin after a no", got)
	}
}

// TestSetupPluginStepSkipsWhenClaudeAbsent pins the drop rule's reach: no
// claude on PATH means no plugin question, so no plugin answer, so no plugin
// step even with yes on everything else.
func TestSetupPluginStepSkipsWhenClaudeAbsent(t *testing.T) {
	standing := setupStandingAnswers(nil, false, false)
	if _, asked := standing["plugin"]; asked {
		t.Fatalf("standing answers %v name a plugin, want none when claude is absent", standing)
	}
	if got := setupStepsFor((Answers)(standing)); slices.Contains(got, "plugin") {
		t.Fatalf("steps = %v, want no plugin step for an absent claude", got)
	}
}

// TestSetupStepsForPinsYes pins the other end of the list: a yes on plugin,
// service and smoke puts each step in apply order after config and agents.
func TestSetupStepsForPinsYes(t *testing.T) {
	answers := Answers{"plugin": []string{"yes"}, "service": []string{"y"}}
	got := setupStepsFor(answers)
	want := []string{"config", "agents", "plugin", "service"}
	if !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

// TestSetupStepsForPinsMusePluginOrder pins where the muse-plugin step
// sits: a yes on every install question gives config, agents, plugin,
// muse-plugin, service in apply order, the muse install between the Claude
// Code one and the service.
func TestSetupStepsForPinsMusePluginOrder(t *testing.T) {
	answers := Answers{"plugin": []string{"yes"}, "muse-plugin": []string{"yes"}, "service": []string{"y"}, "smoke": []string{"no"}}
	got := setupStepsFor(answers)
	want := []string{"config", "agents", "plugin", "muse-plugin", "service"}
	if !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

// TestSetupSmokePlanContent pins the plan the smoke round sends by its
// words: the exact HELLO.md file, the exact line, the commit, and the
// report that closes the round.
func TestSetupSmokePlanContent(t *testing.T) {
	text := smokePlanText()
	for _, want := range []string{"HELLO.md", "relevo works", "commit", "report"} {
		if !strings.Contains(text, want) {
			t.Errorf("smoke plan %q lacks %q", text, want)
		}
	}
}

// TestSetupInventoryLine pins the one probe line: logged in plain, and not
// logged in naming the login command, joined by the interpunct; the notes
// print as their own lines, not part of it.
func TestSetupInventoryLine(t *testing.T) {
	line := "codex: logged in · claude: logged in · agy: on PATH, not logged in -> run agy once to sign in"
	if got := setupInventoryLine(setupInventory()); got != line {
		t.Errorf("inventory line = %q, want %q", got, line)
	}
	// opencode is never asked for a login: its line counts what each
	// provider can run.
	oc := Inventory{Harnesses: []HarnessStatus{{Harness: "opencode", OnPath: true, LoggedIn: true,
		Catalog: []string{"openrouter/a/b", "openrouter/c/d", "opencode/free-1"}}}}
	if got := setupInventoryLine(oc); got != "opencode: openrouter (2 models), opencode (1 free models)" {
		t.Errorf("opencode line = %q", got)
	}
}

func setupInventory() Inventory {
	return Inventory{
		Harnesses: []HarnessStatus{
			{Harness: "codex", OnPath: true, LoggedIn: true},
			{Harness: "claude", OnPath: true, LoggedIn: true},
			{Harness: "agy", OnPath: true, Login: "run agy once to sign in"},
		},
	}
}

// TestSetupStandingAnswersDropsTheirQuestions pins the standing facts: an
// installed plugin and a present unit stand as yes in what the engine
// interprets, so their questions drop while the keepers still parse.
func TestSetupStandingAnswersDropsTheirQuestions(t *testing.T) {
	standing := setupStandingAnswers(nil, true, true)
	if got := standing["plugin"]; !slices.Equal(got, []string{"yes"}) {
		t.Fatalf("plugin standing = %v, want yes", got)
	}
	if got := standing["service"]; !slices.Equal(got, []string{"yes"}) {
		t.Fatalf("service standing = %v, want yes", got)
	}
	inv := setupCodexInventory()
	qs := Questions(inv, standing)
	for _, q := range qs {
		if q.ID == "plugin" || q.ID == "service" {
			t.Fatalf("question %q stood for standing answers %v", q.ID, standing)
		}
	}
}

// TestMergeSetupAnswersPinsPrecedence pins how the apply's answers come
// together: given flags and prompts win, the recommendations fill what no
// answer carried, and a recommendation the stored config keeps stands first.
func TestMergeSetupAnswersPinsPrecedence(t *testing.T) {
	inv := setupCodexInventory()
	qs := Questions(inv, nil)
	rec := RecommendedAnswers(inv, nil)
	given := Answers{"tier": []string{"edit"}}

	answers := mergeSetupAnswers(qs, rec, given)
	if got := answers["tier"]; !slices.Equal(got, []string{"edit"}) {
		t.Fatalf("tier = %v, want the given edit", got)
	}
	if got := answers["smoke"]; !slices.Equal(got, []string{"yes"}) {
		t.Fatalf("smoke = %v, want the recommendation", got)
	}
	if len(answers["builder"]) == 0 {
		t.Fatal("no builder answer, want the recommendation")
	}
}

// TestSetupPromptEnterTakesRecommended pins the TTY answer rules on the
// prompt path: an empty line takes the recommendation, a comma list of
// numbers picks the choices in the order typed, a y/n abbreviates like a
// flag, and three unusable lines end the run.
func TestSetupPromptEnterTakesRecommended(t *testing.T) {
	builder := Question{
		ID: "builder", Prompt: "Which models build code?", Multi: true,
		Recommended: []string{"codex/openai/gpt-6-luna:high", "claude/anthropic/sonnet"},
		Choices: []Choice{
			{Value: "codex/openai/gpt-6-luna:high"},
			{Value: "codex/openai/gpt-5.6-terra"},
			{Value: "claude/anthropic/sonnet"},
		},
	}
	plugin := Question{
		ID: "plugin", Prompt: "Install the relevo plugin?", Recommended: []string{"yes"},
		Choices: []Choice{{Value: "yes"}, {Value: "no"}},
	}
	qs := []Question{builder, plugin}

	in := strings.NewReader("1,3\n" + strings.Repeat("maybe\n", 2) + "n\n")
	out := new(bytes.Buffer)
	answers, err := askSetupQuestions(out, in, qs, nil, nil)
	if err != nil {
		t.Fatalf("askSetupQuestions: %v", err)
	}
	if got := answers["builder"]; !slices.Equal(got, []string{"codex/openai/gpt-6-luna:high", "claude/anthropic/sonnet"}) {
		t.Fatalf("numbers picked %v, want the two choices in typed order", got)
	}
	if got := answers["plugin"]; !slices.Equal(got, []string{"no"}) {
		t.Fatalf("y/n parsed %v, want no", got)
	}
	if !strings.Contains(out.String(), "(recommended)") {
		t.Errorf("prompt %q does not mark the recommendation", out.String())
	}

	// Enter takes the recommendation on its own, and three unusable lines
	// give up on the question.
	in2 := strings.Repeat("maybe\n", 3)
	if _, err := askSetupQuestions(out, strings.NewReader(in2), []Question{builder}, nil, nil); err == nil {
		t.Fatal("three unusable answers parsed, want a failure")
	}
}

// TestSetupPromptEmptyLineTakesRecommendation pins the Enter rule alone: one
// question, one empty line, the recommendation comes back.
func TestSetupPromptEmptyLineTakesRecommendation(t *testing.T) {
	tier := Question{
		ID: "tier", Prompt: "How much may a builder do without asking?",
		Recommended: []string{"yolo"},
		Choices:     []Choice{{Value: "yolo"}, {Value: "edit"}},
	}
	answers, err := askSetupQuestions(new(bytes.Buffer), strings.NewReader("\n"), []Question{tier}, nil, nil)
	if err != nil {
		t.Fatalf("askSetupQuestions: %v", err)
	}
	if got := answers["tier"]; !slices.Equal(got, []string{"yolo"}) {
		t.Fatalf("enter answered %v, want the recommendation yolo", got)
	}
}

// TestSetupPromptAsksEffortAfterModels pins the effort follow-up: models are
// picked once each, each picked model with several efforts gets its own
// effort question after the list, and the answer is echoed back.
func TestSetupPromptAsksEffortAfterModels(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{
		{Harness: "agy", OnPath: true, LoggedIn: true, Models: []string{"gemini-3.8-flash-low", "gemini-3.8-flash-high"}},
		{Harness: "claude", OnPath: true, LoggedIn: true, Models: []string{"sonnet", "opus", "haiku"}},
	}}
	var builder Question
	for _, q := range Questions(inv, nil) {
		if q.ID == "builder" {
			builder = q
		}
	}
	// 1 = gemini-3.8-flash, 4 = haiku, 2 = sonnet; then flash at low (1),
	// haiku at high (3), and Enter keeps sonnet's recommended medium.
	in := strings.NewReader("1,4,2\n1\n3\n\n")
	out := new(bytes.Buffer)
	answers, err := askSetupQuestions(out, in, []Question{builder}, nil, nil)
	if err != nil {
		t.Fatalf("askSetupQuestions: %v\n%s", err, out)
	}
	want := []string{"agy/google/gemini-3.8-flash-low", "claude/anthropic/haiku:high", "claude/anthropic/sonnet:medium"}
	if got := answers["builder"]; !slices.Equal(got, want) {
		t.Fatalf("answers = %v, want %v\n%s", got, want, out)
	}
	for _, line := range []string{"comma-separated", "Effort for gemini-3.8-flash on agy (google)?", "Effort for haiku on claude (anthropic)?",
		"-> gemini-3.8-flash (low), haiku (high), sonnet (medium)"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("prompts lack %q:\n%s", line, out)
		}
	}
	if strings.Contains(out.String(), "gemini-3.8-flash-high (") {
		t.Errorf("an effort variant is listed as its own model:\n%s", out)
	}
}

// TestSetupPromptRolesKeepOrCustomize pins the roles step: the derived roles
// are shown from the answers given, Enter keeps them unasked and unanswered,
// and customize asks each role with its derived pick recommended.
func TestSetupPromptRolesKeepOrCustomize(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{
		{Harness: "claude", OnPath: true, LoggedIn: true, Models: []string{"sonnet", "opus", "haiku"}},
	}}
	var roles []Question
	for _, q := range Questions(inv, nil) {
		if q.Optional {
			roles = append(roles, q)
		}
	}
	given := Answers{"builder": {"claude/anthropic/haiku:low"}, "planner": {"claude/anthropic/opus:high"}}

	out := new(bytes.Buffer)
	answers, err := askSetupQuestions(out, strings.NewReader("\n"), roles, given, nil)
	if err != nil {
		t.Fatalf("keep: %v\n%s", err, out)
	}
	if len(answers) != 0 {
		t.Errorf("keeping answered %v, want the roles left to derive", answers)
	}
	for _, line := range []string{"librarian (writes docs only): haiku (low)", "security (reviews for security issues): opus (high)"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("roles summary lacks %q:\n%s", line, out)
		}
	}

	// customize, then: librarian Enter (haiku, effort Enter), reviewer 2
	// (opus, effort Enter), security Enter, lite-planner Enter.
	out.Reset()
	in := strings.NewReader("2\n\n\n2\n\n\n\n\n\n")
	if answers, err = askSetupQuestions(out, in, roles, given, nil); err != nil {
		t.Fatalf("customize: %v\n%s", err, out)
	}
	if got := answers["librarian"]; !slices.Equal(got, []string{"claude/anthropic/haiku:low"}) {
		t.Errorf("librarian = %v, want the derived haiku:low", got)
	}
	if got := answers["reviewer"]; len(got) != 1 || !strings.HasPrefix(got[0], "claude/anthropic/opus") {
		t.Errorf("reviewer = %v, want opus", got)
	}
}

// TestMergeSetupAnswersLeavesRolesToDerive pins that a role nobody answered
// stays empty even when a recommendation exists, because its recommendation
// follows the recommended builders, not the ones actually picked.
func TestMergeSetupAnswersLeavesRolesToDerive(t *testing.T) {
	qs := []Question{{ID: "librarian", Optional: true, Recommended: []string{"codex/openai/gpt-6-luna:medium"}}}
	if got := mergeSetupAnswers(qs, Answers{"librarian": {"x"}}, nil); len(got["librarian"]) != 0 {
		t.Errorf("merged librarian = %v, want it left to derive", got["librarian"])
	}
	if got := mergeSetupAnswers(qs, nil, Answers{"librarian": {"y"}}); !slices.Equal(got["librarian"], []string{"y"}) {
		t.Errorf("merged librarian = %v, want the given y", got["librarian"])
	}
}

// TestSetupCodedStepErrorKeepsItsRowAndExit pins the classified-transport
// shape: its row carries the full message and next verbatim, and silenceStep
// passes it through so the caller still sees its own exit.
func TestSetupCodedStepErrorKeepsItsRowAndExit(t *testing.T) {
	rows := []struct {
		name string
		in   *CodedStepError
	}{
		{"refused exits 2", &CodedStepError{Message: "builder is busy", Next: "relevo doctor", Code: 2}},
		{"other classes keep their exit", &CodedStepError{Message: "no such round", Next: "relevo doctor", Code: 1}},
		{"multiline message is kept whole", &CodedStepError{Message: "busy\nsecond line", Next: "relevo doctor", Code: 2}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			doc := setupStepDocOf("smoke", row.in)
			if doc.Step != "smoke" || doc.OK {
				t.Fatalf("doc = %+v, want a failed smoke row", doc)
			}
			if doc.Detail != row.in.Message {
				t.Errorf("detail = %q, want the full message %q", doc.Detail, row.in.Message)
			}
			if doc.Next != row.in.Next {
				t.Errorf("next = %q, want %q", doc.Next, row.in.Next)
			}
			in := error(row.in)
			var ce *CodedStepError
			if out := silenceStep(in); !errors.Is(out, row.in) {
				t.Errorf("silenceStep = %v, want the shaped error back unwrapped", out)
			} else if !errors.As(out, &ce) || ce.Code != row.in.Code {
				t.Errorf("silenceStep = %v, want the exit %d preserved", out, row.in.Code)
			}
		})
	}
}
