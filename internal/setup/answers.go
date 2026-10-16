package setup

import (
	"fmt"
	"strings"
)

// ParseAnswer validates one raw value against q's choices and returns the
// values that flow on. A multi question splits on commas, keeping the order;
// a candidate question takes each token as a choice value or a choice value
// plus a trailing `:`-reasoning effort; a yes/no question also takes the
// y/n a TTY prompt produces and hands back the spelled value. A token naming
// no choice is an error that lists the valid values.
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

// matchChoice reports the canonical value one raw token names: a choice
// value verbatim; on a yes/no pair, also the y/n a prompt abbreviates; on a
// candidate question, also a choice with one of its efforts after the model.
// A model answered without an effort runs bare where it can, and at its
// default where agy names no model without one.
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

// yesNoPair reports whether a question's choices are just the yes/no pair.
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

// validValues lists a question's choice values for an error that names them.
func validValues(q Question) string {
	vals := make([]string, 0, len(q.Choices))
	for _, c := range q.Choices {
		vals = append(vals, c.Value)
	}
	return strings.Join(vals, ", ")
}
