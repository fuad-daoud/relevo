package setup

// roleIDs are the roles Build fills from the builder and planner answers
// unless their own optional question is answered: the order a front end
// lists and asks them in.
var roleIDs = []string{"librarian", "reviewer", "security", "lite-planner"}

// RoleWhat says in a few words what each role does, for a summary line.
var RoleWhat = map[string]string{
	"librarian":    "writes docs only",
	"reviewer":     "reviews a builder's diff",
	"security":     "reviews for security issues",
	"lite-planner": "writes small plans",
}

var rolePrompts = map[string]string{
	"librarian":    "Which models write docs? The librarian is a builder that may change only documentation and comments. Pick one or more, comma-separated, in the order to try them",
	"reviewer":     "Who reviews a builder's diff before you merge it?",
	"security":     "Who reviews changes for security issues?",
	"lite-planner": "Who writes small plans, for quick tasks the full planner would be heavy for?",
}

// roleQuestions are the optional role questions. Their recommendations show
// what an unanswered role gets from the recommended builder and planner;
// DerivedRoles gives the same from the answers actually given.
func roleQuestions(inv Inventory, builder, planner Question) []Question {
	cheap := cheapestPick(inv)
	if c, _, ok := choiceAndEffort(builder, cheap); ok {
		cheap = atEffort(c, builderEffort)
	}
	recommended := map[string][]string{
		"librarian": builder.Recommended,
		"security":  planner.Recommended,
	}
	if cheap != "" {
		recommended["reviewer"] = []string{cheap}
		recommended["lite-planner"] = []string{cheap}
	}
	// The lite-planner keeps the OpenRouter lineup's planner wherever
	// opencode stands.
	if c, ok := pickChoice(loggedChoices(inv)["opencode"], "deepseek/", ""); ok {
		recommended["lite-planner"] = []string{c.Value}
	}
	qs := make([]Question, 0, len(roleIDs))
	for _, id := range roleIDs {
		qs = append(qs, Question{
			ID:          id,
			Prompt:      rolePrompts[id],
			Multi:       id == "librarian",
			Optional:    true,
			Recommended: recommended[id],
			Choices:     candidateChoices(inv),
		})
	}
	return qs
}

// IsRole reports whether a question id is one of the derived roles.
func IsRole(id string) bool {
	for _, r := range roleIDs {
		if r == id {
			return true
		}
	}
	return false
}

// DerivedRoles is what each role gets when its question goes unanswered:
// the librarian writes with the builders, security reviews with the
// planner, and the reviewer and lite-planner take the model their own
// question recommends -- at the effort the answers already picked it at,
// when they did.
func DerivedRoles(qs []Question, answers Answers) Answers {
	out := Answers{}
	if b := answers["builder"]; len(b) > 0 {
		out["librarian"] = b
	}
	if p := answers["planner"]; len(p) > 0 {
		out["security"] = p
	}
	picked := append(append([]string{}, answers["builder"]...), answers["planner"]...)
	for _, q := range qs {
		if (q.ID == "reviewer" || q.ID == "lite-planner") && len(q.Recommended) > 0 {
			out[q.ID] = []string{pickedAlready(q, picked, q.Recommended[0])}
		}
	}
	return out
}

// pickedAlready is a derived pick as the answers already picked its model,
// at whatever effort they chose, so one model never lands as two candidates
// that differ only in effort.
func pickedAlready(q Question, picked []string, derived string) string {
	want, ok := ChoiceFor(q, derived)
	if !ok {
		return derived
	}
	for _, t := range picked {
		if c, ok := ChoiceFor(q, t); ok && c.Value == want.Value {
			return t
		}
	}
	return derived
}

// Describe is one answer value as a person reads it: the model with its
// effort in brackets, or the plain value for a question without models.
func Describe(q Question, value string) string {
	c, effort, ok := choiceAndEffort(q, value)
	if !ok || c.sep == "" {
		return value
	}
	if effort == "" && !c.bare {
		effort = c.effort
	}
	if effort == "" {
		return c.Label
	}
	return c.Label + " (" + effort + ")"
}
