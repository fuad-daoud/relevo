package setup

import (
	"slices"
	"strings"
)

// effortWords are the effort names the harnesses spell, lightest first: the
// order an agy group lists its efforts in, and the suffixes agy names each
// effort's model with.
var effortWords = []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

var effortWhy = map[string]string{
	"minimal": "fastest; almost no reasoning",
	"low":     "fast, light reasoning",
	"medium":  "balanced speed and depth",
	"high":    "deeper reasoning for complex work; slower",
	"xhigh":   "extra-deep reasoning; slower still",
	"max":     "deepest reasoning; slowest and uses the most quota",
	"ultra":   "deepest reasoning plus automatic task delegation",
}

// claudeEfforts is what `claude --effort` takes, for each of its aliases.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// harnessChoices are one harness's models as the candidate questions list
// them: one choice per model, its efforts carried on the choice rather than
// listed as models of their own.
func harnessChoices(s HarnessStatus) []Choice {
	why := "on " + s.Harness + " (" + providerFor(s.Harness) + ")"
	var out []Choice
	add := func(c Choice) {
		c.Value = refFor(s.Harness, c.Label)
		c.Why = why
		out = append(out, c)
	}
	switch s.Harness {
	case "agy":
		for _, c := range agyChoices(s.Models) {
			add(c)
		}
	case "opencode":
		return opencodeChoices(s)
	case "claude":
		for _, m := range s.Models {
			add(Choice{Label: m, Efforts: claudeEfforts, effort: "medium", sep: ":", bare: true})
		}
	default:
		for _, m := range s.Models {
			efforts := s.Efforts[m]
			add(Choice{Label: m, Efforts: efforts, effort: s.DefaultEffort[m], sep: ":", bare: true, open: len(efforts) == 0})
		}
	}
	return out
}

// agyChoices groups agy's models by the effort its names end in, because
// agy lists each effort as a model of its own: gemini-3.8-flash-high and
// gemini-3.8-flash-low are one choice at two efforts. A model with no effort
// suffix stands as itself.
func agyChoices(models []string) []Choice {
	var out []Choice
	at := map[string]int{}
	for _, m := range models {
		base, effort := splitAgyEffort(m)
		i, seen := at[base]
		if !seen {
			i = len(out)
			at[base] = i
			out = append(out, Choice{Label: base, sep: "-"})
		}
		if effort == "" {
			out[i].bare = true
			continue
		}
		out[i].Efforts = append(out[i].Efforts, effort)
	}
	for i := range out {
		slices.SortStableFunc(out[i].Efforts, func(a, b string) int {
			return slices.Index(effortWords, a) - slices.Index(effortWords, b)
		})
		out[i].effort = preferredEffort(out[i].Efforts)
	}
	return out
}

func splitAgyEffort(model string) (base, effort string) {
	i := strings.LastIndexByte(model, '-')
	if i <= 0 || !slices.Contains(effortWords, model[i+1:]) {
		return model, ""
	}
	return model[:i], model[i+1:]
}

// preferredEffort is the effort an agy group runs at when none is picked:
// medium where it is offered, else the lightest.
func preferredEffort(efforts []string) string {
	if slices.Contains(efforts, "medium") || len(efforts) == 0 {
		return "medium"
	}
	return efforts[0]
}

// valueAt is a choice's answer value at one effort; no effort leaves a model
// that runs bare as it is, and gives an agy group its default.
func valueAt(c Choice, effort string) string {
	if effort == "" {
		if c.bare || c.sep == "" {
			return c.Value
		}
		effort = c.effort
	}
	return c.Value + c.sep + effort
}

// atEffort is a choice's value at a wanted effort when the choice runs at
// it, else at its own default: the recommendations ask for one effort across
// harnesses that do not all offer it.
func atEffort(c Choice, want string) string {
	if slices.Contains(c.Efforts, want) || c.open {
		return valueAt(c, want)
	}
	return valueAt(c, "")
}

// choiceAndEffort finds the choice a value names and the effort it carries:
// the choice value itself, or the value plus an effort after ":" -- or after
// "-", where agy spells it in the model name.
func choiceAndEffort(q Question, value string) (Choice, string, bool) {
	all := append(append([]Choice{}, q.Choices...), q.more...)
	for _, c := range all {
		if value == c.Value {
			return c, "", true
		}
	}
	for _, c := range all {
		if c.sep == "" {
			continue
		}
		for _, sep := range []string{":", c.sep} {
			effort, ok := strings.CutPrefix(value, c.Value+sep)
			if !ok || effort == "" {
				continue
			}
			if slices.Contains(c.Efforts, effort) || (c.open && sep == c.sep) {
				return c, effort, true
			}
		}
	}
	return Choice{}, "", false
}

// ChoiceFor is the choice one answer value picks, whatever effort it
// carries.
func ChoiceFor(q Question, value string) (Choice, bool) {
	c, _, ok := choiceAndEffort(q, value)
	return c, ok
}

// builderEffort and plannerEffort are the efforts each question recommends:
// a builder follows a plan whose thinking is already done, so medium spends
// less quota on the same work; the planner does that thinking.
const (
	builderEffort = "medium"
	plannerEffort = "high"
)

// effortFor is the effort one candidate question recommends.
func effortFor(q Question) string {
	if q.ID == "planner" {
		return plannerEffort
	}
	return builderEffort
}

// EffortQuestion is the follow-up a picked model gets when it runs at more
// than one effort: which one, recommending the effort the pick already
// carries, else the one a recommendation of the same model carries, else
// the question's own effort, else the model's default.
func EffortQuestion(q Question, picked string) (Question, bool) {
	c, effort, ok := choiceAndEffort(q, picked)
	if !ok || len(c.Efforts) < 2 {
		return Question{}, false
	}
	if effort == "" {
		effort = recommendedEffort(q, c)
	}
	if !slices.Contains(c.Efforts, effort) {
		effort = preferredEffort(c.Efforts)
	}
	choices := make([]Choice, 0, len(c.Efforts))
	for _, e := range c.Efforts {
		choices = append(choices, Choice{Value: e, Why: effortWhy[e]})
	}
	return Question{
		ID:          q.ID + "-effort",
		Prompt:      "Effort for " + c.Label + " " + c.Why + "?",
		Recommended: []string{effort},
		Choices:     choices,
	}, true
}

func recommendedEffort(q Question, c Choice) string {
	for _, r := range q.Recommended {
		if rc, e, ok := choiceAndEffort(q, r); ok && rc.Value == c.Value && e != "" {
			return e
		}
	}
	if want := effortFor(q); slices.Contains(c.Efforts, want) {
		return want
	}
	return c.effort
}

// WithEffort is a picked value moved to one effort.
func WithEffort(q Question, picked, effort string) string {
	c, _, ok := choiceAndEffort(q, picked)
	if !ok {
		return picked
	}
	return valueAt(c, effort)
}
