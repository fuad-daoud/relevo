package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// namePattern is the actor-name shape a workflow, a step and an actor share.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidName reports whether name is a valid workflow or actor name.
func ValidName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("workflow: bad name %q", name)
	}
	return nil
}

// RenderParams replaces each {{params.x}} in s with that param's value and
// leaves every other reference, including an unknown params one, untouched.
func RenderParams(def Definition, s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		start := strings.Index(s[i:], "{{")
		if start < 0 {
			b.WriteString(s[i:])
			break
		}
		start += i
		b.WriteString(s[i:start])
		end := strings.Index(s[start+2:], "}}")
		if end < 0 {
			b.WriteString(s[start:])
			break
		}
		whole := s[start : start+2+end+2]
		inner := strings.TrimSpace(s[start+2 : start+2+end])
		if p, ok := paramNamed(def, inner); ok {
			b.WriteString(p.render())
		} else {
			b.WriteString(whole)
		}
		i = start + 2 + end + 2
	}
	return b.String()
}

// paramNamed returns the param an inner reference text names, when it is a
// params reference to a param the workflow declares.
func paramNamed(def Definition, inner string) (Param, bool) {
	name, ok := strings.CutPrefix(inner, "params.")
	if !ok {
		return Param{}, false
	}
	p, ok := def.Params[name]
	return p, ok
}

// render is a param's inline value.
func (p Param) render() string {
	switch p.Kind {
	case ParamBool:
		return strconv.FormatBool(p.Bool)
	case ParamInt:
		return strconv.Itoa(p.Int)
	case ParamString:
		return p.Str
	default:
		return ""
	}
}

// WithParams returns def with the named params replaced by values parsed into
// each param's own kind. An unknown key is an error that lists the params the
// workflow takes, sorted.
func WithParams(def Definition, values map[string]string) (Definition, error) {
	if len(values) == 0 {
		return def, nil
	}
	params := make(map[string]Param, len(def.Params))
	for key, p := range def.Params {
		params[key] = p
	}
	out := def
	out.Params = params
	for _, key := range sortedKeys(values) {
		p, ok := params[key]
		if !ok {
			return Definition{}, fmt.Errorf("workflow: unknown param %q; the workflow takes: %s", key, strings.Join(sortedKeys(def.Params), ", "))
		}
		parsed, err := p.withValue(values[key])
		if err != nil {
			return Definition{}, fmt.Errorf("workflow: param %s: %w", key, err)
		}
		params[key] = parsed
	}
	return out, nil
}

// withValue parses a string into the param's kind.
func (p Param) withValue(s string) (Param, error) {
	switch p.Kind {
	case ParamBool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return Param{}, fmt.Errorf("want bool: %w", err)
		}
		return Param{Kind: ParamBool, Bool: b}, nil
	case ParamInt:
		i, err := strconv.Atoi(s)
		if err != nil {
			return Param{}, fmt.Errorf("want int: %w", err)
		}
		return Param{Kind: ParamInt, Int: i}, nil
	case ParamString:
		return Param{Kind: ParamString, Str: s}, nil
	default:
		return Param{}, fmt.Errorf("unknown param kind %q", string(p.Kind))
	}
}
