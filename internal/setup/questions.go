package setup

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// Choice is one of a question's answers: its value -- what an --answer flag
// takes -- a short label, and a one-line why.
type Choice struct {
	Value string
	Label string
	Why   string
}

// Question is one thing the wizard asks: its prompt, the choices to show,
// the values recommended (one, or several in order for a multi), and whether
// several may be picked in order.
type Question struct {
	ID          string
	Prompt      string
	Multi       bool
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
var questionKinds = []string{"codex", "agy", "claude"}

// tierPrompt says plainly what each tier lets a builder do, because nothing
// else in the flow may hide it.
const tierPrompt = "How much may a builder do without asking? yolo lets them run any command in their worktree without asking; at edit they change files but a claude builder cannot run tests or git"

// Questions are the questions still open, in the order the front end asks
// them: builder, planner, tier, then the optional plugin and service, then
// smoke. A no-longer-applicable one is dropped rather than carried with an
// empty choice list.
func Questions(inv Inventory, answers map[string][]string) []Question {
	qs := []Question{
		builderQuestion(inv),
		plannerQuestion(inv),
		tierQuestion(),
	}
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
// recommendation. current is nil on a fresh machine, where every value is
// the default.
func RecommendedAnswers(inv Inventory, current map[string][]string) Answers {
	out := Answers{}
	for _, q := range Questions(inv, nil) {
		if kept, ok := keptAnswers(q, current); ok {
			out[q.ID] = kept
			continue
		}
		out[q.ID] = q.Recommended
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
// order, and recommends one model per harness: codex's first listed model at
// high reasoning, agy's first flash-high model else its first, claude sonnet.
func builderQuestion(inv Inventory) Question {
	models := loggedModels(inv)
	recommended := []string{}
	if len(models["codex"]) > 0 {
		recommended = append(recommended, refFor("codex", models["codex"][0]+":high"))
	}
	if m, ok := agyFlashPick(models["agy"]); ok {
		recommended = append(recommended, refFor("agy", m))
	}
	if len(models["claude"]) > 0 {
		recommended = append(recommended, refFor("claude", "sonnet"))
	}
	return Question{
		ID:          "builder",
		Prompt:      "Which models build code?",
		Multi:       true,
		Recommended: recommended,
		Choices:     candidateChoices(inv),
	}
}

// plannerQuestion names who writes plans: claude opus at medium reasoning
// when it is logged in, else codex's first listed model at high, else agy's
// first claude-opus-* model else its first.
func plannerQuestion(inv Inventory) Question {
	models := loggedModels(inv)
	q := Question{
		ID:      "planner",
		Prompt:  "Who writes plans?",
		Choices: candidateChoices(inv),
	}
	switch {
	case len(models["claude"]) > 0:
		q.Recommended = []string{refFor("claude", "opus:medium")}
	case len(models["codex"]) > 0:
		q.Recommended = []string{refFor("codex", models["codex"][0]+":high")}
	case len(models["agy"]) > 0:
		m, ok := agyPlannerPick(models["agy"])
		if ok {
			q.Recommended = []string{refFor("agy", m)}
		}
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
// logged-in harness offers, question order. The label is the model token and
// the why names the seat it runs on.
func candidateChoices(inv Inventory) []Choice {
	choices := make([]Choice, 0, 8)
	for _, ref := range offeredRefs(inv) {
		r, _ := candidate.ParseRef(ref) // an offered ref always parses
		choices = append(choices, Choice{
			Value: ref,
			Label: r.Model,
			Why:   "on " + r.Harness + " (" + r.Provider + ")",
		})
	}
	return choices
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

// offeredRefs are the candidate values the questions list, question order.
func offeredRefs(inv Inventory) []string {
	models := loggedModels(inv)
	var refs []string
	for _, kind := range questionKinds {
		for _, m := range models[kind] {
			refs = append(refs, refFor(kind, m))
		}
	}
	return refs
}

// refFor prefixes the provider a harness's candidates carry to one model:
// the kind's own first provider.
func refFor(kind, model string) string {
	return kind + "/" + providerFor(kind) + "/" + model
}

// providerFor is the provider a candidate lists under for one harness kind:
// the first of the kind's own providers, with openai spelled for codex,
// whose table's row names none.
func providerFor(kind string) string {
	if kind == "codex" {
		return "openai"
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

// agyPlannerPick is agy's first claude-opus-* model, else its first model.
func agyPlannerPick(models []string) (string, bool) {
	return pickPattern(models, "claude-opus-", "")
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
