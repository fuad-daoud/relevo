package setup

import (
	"fmt"
	"strings"
)

// ParseAnswer validates one raw value against q's choices. A multi question
// splits on commas, keeping the order; a candidate token may carry a `:`-effort;
// a yes/no question also takes a TTY's y/n. A token naming no choice errors,
// listing the valid values.
func ParseAnswer(q Question, raw string) ([]string, error) {
	var tokens []string
	if q.Multi {
		for _, t := range strings.Split(raw, ",") {
			tokens = append(tokens, strings.TrimSpace(t))
		}
	} else {
		tokens = []string{strings.TrimSpace(raw)}
	}
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		m, ok := matchChoice(q, t)
		if !ok {
			return nil, fmt.Errorf("invalid %s value %q; valid values: %s", q.ID, t, validValues(q))
		}
		out = append(out, m)
	}
	return out, nil
}

// matchChoice reports the canonical value one raw token names. A model
// answered without an effort runs bare where it can, and at its default
// where agy names no model without one.
func matchChoice(q Question, token string) (string, bool) {
	if token == "" {
		return "", false
	}
	if yesNoPair(q) {
		switch strings.ToLower(token) {
		case "y", "yes":
			return "yes", true
		case "n", "no":
			return "no", true
		}
		return "", false
	}
	c, effort, ok := choiceAndEffort(q, token)
	if !ok {
		return "", false
	}
	return valueAt(c, effort), true
}

func yesNoPair(q Question) bool {
	if len(q.Choices) != 2 {
		return false
	}
	for _, c := range q.Choices {
		if c.Value != "yes" && c.Value != "no" {
			return false
		}
	}
	return true
}

func validValues(q Question) string {
	vals := make([]string, 0, len(q.Choices))
	for _, c := range q.Choices {
		vals = append(vals, c.Value)
	}
	return strings.Join(vals, ", ")
}
