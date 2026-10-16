package setup

import (
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// Choice is one of a question's answers: its value -- what an --answer flag
// takes -- a short label, a one-line why, and for a model the efforts it runs
// at. The unexported fields say how an effort joins the value: its default,
// the separator, whether the model also runs with none, and whether the
// effort list is unknown so any effort passes.
type Choice struct {
	Value   string
	Label   string
	Why     string
	Efforts []string

	effort string
	sep    string
	bare   bool
	open   bool
}

// Question is one thing the wizard asks: its prompt, the choices to show,
// the values recommended (one, or several in order for a multi), whether
// several may be picked in order, and whether it may go unanswered -- a role
// question, whose role Build then derives.
type Question struct {
	ID          string
	Prompt      string
	Multi       bool
	Optional    bool
	Recommended []string
	Choices     []Choice
}

// Answers maps a question id to the values it already carries: candidate,
// planner and tier values a stored config keeps -- a rerun recommends them
// again while they are still among the choices -- and for plugin and service
// the presence of an answer standing in for the fact the question asks
// about, so the question itself is dropped.
type Answers map[string][]string

// questionKinds is the order the questions name harnesses in: the first-pick
// order for builder choices and their recommendations.
var questionKinds = []string{"codex", "agy", "claude", "opencode"}

// builderPrompt names the comma list, because a builder answer is several
// models in the order they are tried.
const builderPrompt = "Which models build code? Pick one or more, comma-separated, in the order to try them: the first builds, the next takes over when one hits a usage limit"

// tierPrompt says plainly what each tier lets a builder do, because nothing
// else in the flow may hide it.
const tierPrompt = "How much may a builder do without asking? yolo lets them run any command in their worktree without asking; at edit they change files but a claude builder cannot run tests or git"

// Questions are the questions still open, in the order the front end asks
// them: builder, planner, the optional roles, tier, then the optional plugin
// and service, then smoke. A no-longer-applicable one is dropped rather than
// carried with an empty choice list.
func Questions(inv Inventory, answers map[string][]string) []Question {
	builder, planner := builderQuestion(inv), plannerQuestion(inv)
	qs := append([]Question{builder, planner}, roleQuestions(inv, builder, planner)...)
	qs = append(qs, tierQuestion())
	if claudeOnPath(inv) && len(answers["plugin"]) == 0 {
		qs = append(qs, yesNoQuestion("plugin",
			"Install the relevo plugin into Claude Code (the MasterMind side)?",
			"the plugin gives Claude Code the relevo guide and verbs",
			"skip it; the same two commands install it later"))
	}
	if len(answers["service"]) == 0 {
		qs = append(qs, yesNoQuestion("service",
			"Run the relevo daemon as a user service (systemd on Linux, launchd on macOS)?",
			"the service keeps the daemon ready without a shell of yours",
			"relevo starts the daemon on demand"))
	}
	qs = append(qs, yesNoQuestion("smoke", "Run one small real round now?",
		"it proves the install end to end in a minute or two",
		"you can run relevo doctor later instead"))
	for i, q := range qs {
		if kept, ok := keptAnswers(q, answers); ok {
			qs[i].Recommended = kept
		}
	}
	return qs
}

// RecommendedAnswers are every question's answer together, ready for Build:
// for each one the caller already keeps values, those stand while they are
// still among the question's choices; every other question takes its own
// recommendation. A role question takes none, so Build derives the role
// from the answers it stands beside. current is nil on a fresh machine,
// where every value is the default.
func RecommendedAnswers(inv Inventory, current map[string][]string) Answers {
	out := Answers{}
	for _, q := range Questions(inv, nil) {
		if kept, ok := keptAnswers(q, current); ok {
			out[q.ID] = kept
			continue
		}
		if !q.Optional {
			out[q.ID] = q.Recommended
		}
	}
	return out
}

// keptAnswers are the values an already-standing answer keeps: each one
// still matching one of the question's choices, in the order given.
func keptAnswers(q Question, answers map[string][]string) ([]string, bool) {
	vals := answers[q.ID]
	if len(vals) == 0 {
		return nil, false
	}
	kept := make([]string, 0, len(vals))
	for _, v := range vals {
		m, ok := matchChoice(q, v)
		if !ok {
			continue
		}
		kept = append(kept, m)
	}
	if len(kept) == 0 {
		return nil, false
	}
	return kept, true
}

// builderQuestion shows every model each logged-in harness offers, question
// order, and recommends one model per harness at medium effort: codex's
// first listed model, agy's first gemini flash model else its first, claude
// sonnet.
func builderQuestion(inv Inventory) Question {
	offers := loggedChoices(inv)
	recommended := []string{}
	if cs := offers["codex"]; len(cs) > 0 {
		recommended = append(recommended, atEffort(cs[0], builderEffort))
	}
	if c, ok := pickChoice(offers["agy"], "gemini-", "flash"); ok {
		recommended = append(recommended, atEffort(c, builderEffort))
	}
	if c, ok := pickChoice(offers["claude"], "sonnet", ""); ok {
		recommended = append(recommended, atEffort(c, builderEffort))
	}
	if cs := offers["opencode"]; len(cs) > 0 {
		recommended = append(recommended, cs[0].Value)
	}
	return Question{
		ID:          "builder",
		Prompt:      builderPrompt,
		Multi:       true,
		Recommended: recommended,
		Choices:     candidateChoices(inv),
	}
}

// plannerQuestion names who writes plans, at high effort: claude opus when
// it is logged in, else codex's first listed model, else agy's first
// claude-opus-* model else its first.
func plannerQuestion(inv Inventory) Question {
	offers := loggedChoices(inv)
	q := Question{
		ID:      "planner",
		Prompt:  "Who writes plans?",
		Choices: candidateChoices(inv),
	}
	if c, ok := pickChoice(offers["claude"], "opus", ""); ok {
		q.Recommended = []string{atEffort(c, plannerEffort)}
	} else if cs := offers["codex"]; len(cs) > 0 {
		q.Recommended = []string{atEffort(cs[0], plannerEffort)}
	} else if c, ok := pickChoice(offers["agy"], "claude-opus-", ""); ok {
		q.Recommended = []string{atEffort(c, plannerEffort)}
	} else if c, ok := pickChoice(offers["opencode"], "deepseek/", ""); ok {
		q.Recommended = []string{c.Value}
	}
	return q
}

// tierQuestion carries both tiers with the prompt naming what each means.
func tierQuestion() Question {
	return Question{
		Recommended: []string{"yolo"},
		ID:          "tier",
		Prompt:      tierPrompt,
		Choices: []Choice{
			{Value: "yolo", Label: "yolo", Why: "builders run tests, git and any command in their worktree"},
			{Value: "edit", Label: "edit", Why: "builders only edit files; a claude builder cannot run tests or git"},
		},
	}
}

// yesNoQuestion is one install-or-not question, recommended yes.
func yesNoQuestion(id, prompt, yesWhy, noWhy string) Question {
	return Question{
		Recommended: []string{"yes"},
		ID:          id,
		Prompt:      prompt,
		Choices: []Choice{
			{Value: "yes", Label: "yes", Why: yesWhy},
			{Value: "no", Label: "no", Why: noWhy},
		},
	}
}

// candidateChoices are the questions' candidate picks, one per model each
// logged-in harness offers, question order. The label is the model and the
// why names the seat it runs on.
func candidateChoices(inv Inventory) []Choice {
	offers := loggedChoices(inv)
	choices := make([]Choice, 0, 8)
	for _, kind := range questionKinds {
		choices = append(choices, offers[kind]...)
	}
	return choices
}

// loggedChoices maps each logged-in wizard harness to its choices.
func loggedChoices(inv Inventory) map[string][]Choice {
	out := make(map[string][]Choice, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		if s.LoggedIn {
			out[s.Harness] = harnessChoices(s)
		}
	}
	return out
}

// pickChoice is the first choice whose model has a given prefix -- and, when
// one is named, a given substring -- else the first with the prefix, else
// the first at all.
func pickChoice(choices []Choice, prefix, contains string) (Choice, bool) {
	labels := make([]string, len(choices))
	for i, c := range choices {
		labels[i] = c.Label
	}
	m, ok := pickPattern(labels, prefix, contains)
	if !ok {
		return Choice{}, false
	}
	return choices[slices.Index(labels, m)], true
}

// claudeOnPath reports whether claude has a status entry in the inventory;
// the plugin question only exists when it does.
func claudeOnPath(inv Inventory) bool {
	for _, s := range inv.Harnesses {
		if s.Harness == "claude" {
			return true
		}
	}
	return false
}

// loggedModels maps each wizard harness kind to the models it offers when it
// is logged in; kinds not in the inventory map to nothing.
func loggedModels(inv Inventory) map[string][]string {
	models := make(map[string][]string, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		if s.LoggedIn {
			models[s.Harness] = s.Models
		}
	}
	return models
}

// refFor prefixes the provider a harness's candidates carry to one model:
// the kind's own first provider.
func refFor(kind, model string) string {
	return kind + "/" + providerFor(kind) + "/" + model
}

// providerFor is the provider a candidate lists under for one harness kind:
// the first of the kind's own providers, with openai spelled for codex,
// whose table's row names none, and openrouter for opencode, whose lineup
// is OpenRouter's.
func providerFor(kind string) string {
	switch kind {
	case "codex":
		return "openai"
	case "opencode":
		return "openrouter"
	}
	h, ok := harnessLookup(kind)
	if !ok || len(h.Providers) == 0 {
		return kind
	}
	return h.Providers[0]
}

func harnessLookup(kind string) (harness.Harness, bool) {
	return harness.Lookup(kind)
}

// agyFlashPick is agy's first gemini-* flash-high model, else its first
// gemini model at all.
func agyFlashPick(models []string) (string, bool) {
	return pickPattern(models, "gemini-", "flash-high")
}

// pickPattern is the first model with a given prefix -- and, when one is
// named, a given substring -- else the first model at all.
func pickPattern(models []string, prefix, contains string) (string, bool) {
	for _, m := range models {
		if strings.HasPrefix(m, prefix) && (contains == "" || strings.Contains(m, contains)) {
			return m, true
		}
	}
	for _, m := range models {
		if strings.HasPrefix(m, prefix) {
			return m, true
		}
	}
	if len(models) > 0 {
		return models[0], true
	}
	return "", false
}
