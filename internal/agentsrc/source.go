// Package agentsrc parses, validates and formats relevo's single-source agent
// format and renders one native file per harness kind from it. The shipped
// agents under internal/harness/agents are hand-maintained per kind.
package agentsrc

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// ErrBadSource is wrapped by every Parse, Validate and Render error.
var ErrBadSource = errors.New("bad agent source")

// Shape distinguishes an agent that may change the tree from one that may
// write only its artifact directory; it does not change the rendered tools.
type Shape string

const (
	ShapeWriter Shape = "writer"
	ShapeReader Shape = "reader"
)

// Source is one custom agent: frontmatter plus prompt body.
type Source struct {
	Name        string
	Description string // one line, at most 300 runes
	Shape       Shape
	Output      string
	Requires    []string
	Kinds       []string
	Body        string // after Parse, ends with exactly one "\n"
}

var (
	nameRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	outputRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	requiredKeys = []string{"name", "description", "shape", "output"}
)

func syntaxErr(line int, what string) error {
	return fmt.Errorf("agent source: line %d: %s: %w", line, what, ErrBadSource)
}

func fieldErr(name, field, what string) error {
	return fmt.Errorf("agent source %s: %s: %s: %w", name, field, what, ErrBadSource)
}

// Parse reads the source text format and validates every field; on success the
// body ends with exactly one "\n" and every error wraps ErrBadSource.
func Parse(data []byte) (Source, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return Source{}, syntaxErr(1, "missing opening fence ---")
	}
	lines := strings.Split(text[len("---\n"):], "\n")

	close, err := closingFence(lines)
	if err != nil {
		return Source{}, err
	}
	s, err := parseFrontmatter(lines[:close], close+2)
	if err != nil {
		return Source{}, err
	}
	s.Body = normaliseBody(strings.Join(lines[close+1:], "\n"))
	if err := s.Validate(); err != nil {
		return Source{}, err
	}
	return s, nil
}

func closingFence(lines []string) (int, error) {
	for i, ln := range lines {
		if ln == "---" {
			return i, nil
		}
	}
	return 0, syntaxErr(len(lines)+1, "missing closing fence ---")
}

// parseFrontmatter reads the key: value lines before the closing fence.
// missingKeyLine is the line number a missing-required-key error names.
func parseFrontmatter(lines []string, missingKeyLine int) (Source, error) {
	var s Source
	s.Requires = []string{}
	s.Kinds = []string{}
	seen := make(map[string]bool, len(requiredKeys)+2)
	for i, raw := range lines {
		lineNo := i + 2 // line 1 is the opening fence
		if strings.TrimSpace(raw) == "" {
			return Source{}, syntaxErr(lineNo, "blank line in frontmatter")
		}
		idx := strings.Index(raw, ":")
		if idx <= 0 {
			return Source{}, syntaxErr(lineNo, "not a key: value line")
		}
		key := strings.TrimSpace(raw[:idx])
		if key == "" {
			return Source{}, syntaxErr(lineNo, "not a key: value line")
		}
		value := strings.TrimSpace(raw[idx+1:])
		if !isKnownKey(key) {
			return Source{}, syntaxErr(lineNo, fmt.Sprintf("unknown key %q", key))
		}
		if seen[key] {
			return Source{}, syntaxErr(lineNo, fmt.Sprintf("duplicate key %q", key))
		}
		seen[key] = true
		switch key {
		case "name":
			s.Name = value
		case "description":
			s.Description = value
		case "shape":
			s.Shape = Shape(value)
		case "output":
			s.Output = value
		case "requires":
			items, err := parseList(value)
			if err != nil {
				return Source{}, syntaxErr(lineNo, err.Error())
			}
			s.Requires = items
		case "kinds":
			items, err := parseList(value)
			if err != nil {
				return Source{}, syntaxErr(lineNo, err.Error())
			}
			s.Kinds = items
		}
	}
	for _, key := range requiredKeys {
		if !seen[key] {
			return Source{}, syntaxErr(missingKeyLine, fmt.Sprintf("missing required key %q", key))
		}
	}
	return s, nil
}

func normaliseBody(body string) string {
	body = strings.TrimPrefix(body, "\n")
	return strings.TrimRight(body, "\n") + "\n"
}

func isKnownKey(key string) bool {
	switch key {
	case "name", "description", "shape", "output", "requires", "kinds":
		return true
	}
	return false
}

// parseList reads a flow list: "[]" or "[x, y]", items trimmed and non-empty.
func parseList(v string) ([]string, error) {
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, errors.New("list must be [] or [x, y]")
	}
	inner := v[1 : len(v)-1]
	if strings.TrimSpace(inner) == "" {
		return []string{}, nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		item := strings.TrimSpace(p)
		if item == "" {
			return nil, errors.New("list item is empty")
		}
		out = append(out, item)
	}
	return out, nil
}

// Validate checks every field rule of the source format.
func (s Source) Validate() error {
	if !nameRe.MatchString(s.Name) {
		return fieldErr(s.Name, "name", "must match "+nameRe.String())
	}
	if s.Description == "" {
		return fieldErr(s.Name, "description", "must not be empty")
	}
	if strings.ContainsAny(s.Description, "\n\r") {
		return fieldErr(s.Name, "description", "must be one line")
	}
	if utf8.RuneCountInString(s.Description) > 300 {
		return fieldErr(s.Name, "description", "must be at most 300 runes")
	}
	if s.Shape != ShapeWriter && s.Shape != ShapeReader {
		return fieldErr(s.Name, "shape", "must be writer or reader")
	}
	if !outputRe.MatchString(s.Output) {
		return fieldErr(s.Name, "output", "must match "+outputRe.String())
	}
	seenReq := make(map[string]bool, len(s.Requires))
	for _, r := range s.Requires {
		if !nameRe.MatchString(r) {
			return fieldErr(s.Name, "requires", fmt.Sprintf("%q is not a valid agent name", r))
		}
		if r == s.Name {
			return fieldErr(s.Name, "requires", "must not require itself")
		}
		if seenReq[r] {
			return fieldErr(s.Name, "requires", fmt.Sprintf("duplicate %q", r))
		}
		seenReq[r] = true
	}
	seenKind := make(map[string]bool, len(s.Kinds))
	for _, k := range s.Kinds {
		if _, ok := harness.Lookup(k); !ok {
			return fieldErr(s.Name, "kinds", fmt.Sprintf("unknown kind %q", k))
		}
		if seenKind[k] {
			return fieldErr(s.Name, "kinds", fmt.Sprintf("duplicate %q", k))
		}
		seenKind[k] = true
	}
	for _, k := range RenderedKinds(s) {
		if harness.IsShipped(k, s.Name) {
			return fieldErr(s.Name, "name", fmt.Sprintf("name %q is a shipped agent; duplicate it under another name", s.Name))
		}
	}
	if slices.Contains(RenderedKinds(s), "codex") && strings.Contains(s.Body, "'''") {
		return fieldErr(s.Name, "body", "must not contain ''' when codex is rendered")
	}
	if strings.TrimSpace(s.Body) == "" {
		return fieldErr(s.Name, "body", "must not be blank")
	}
	return nil
}

// RenderedKinds is the kind list a source renders to: its own kinds, or every
// known kind when Kinds is empty. Exported for internal/roles.
func RenderedKinds(s Source) []string {
	if len(s.Kinds) > 0 {
		return s.Kinds
	}
	all := harness.All()
	out := make([]string, 0, len(all))
	for _, h := range all {
		out = append(out, h.Kind)
	}
	return out
}

// Format writes the source text format; Parse(Format(s)) equals s for any
// valid s whose Body already ends in exactly one "\n".
func Format(s Source) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + s.Name + "\n")
	b.WriteString("description: " + s.Description + "\n")
	b.WriteString("shape: " + string(s.Shape) + "\n")
	b.WriteString("output: " + s.Output + "\n")
	b.WriteString("requires: " + formatList(s.Requires) + "\n")
	b.WriteString("kinds: " + formatList(s.Kinds) + "\n")
	b.WriteString("---\n\n")
	b.WriteString(s.Body)
	return []byte(b.String())
}

// formatList writes a flow list: "[]" or "[a, b]".
func formatList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	return "[" + strings.Join(items, ", ") + "]"
}
