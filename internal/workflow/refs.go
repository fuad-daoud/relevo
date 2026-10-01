package workflow

import (
	"fmt"
	"strings"
)

// Ref is one {{...}} reference: its first segment and the rest.
type Ref struct {
	Root string
	Attr string
}

// Refs returns every reference a template carries, with inner spaces trimmed.
// An unterminated or empty reference is an error.
func Refs(template string) ([]Ref, error) {
	var refs []Ref
	for i := 0; i < len(template); {
		start := strings.Index(template[i:], "{{")
		if start < 0 {
			break
		}
		start += i
		end := strings.Index(template[start+2:], "}}")
		if end < 0 {
			return nil, fmt.Errorf("workflow: unterminated reference at %q", snippet(template[start:]))
		}
		inner := strings.TrimSpace(template[start+2 : start+2+end])
		if inner == "" {
			return nil, fmt.Errorf("workflow: empty reference at %q", snippet(template[start:start+2+end+2]))
		}
		refs = append(refs, parseRef(inner))
		i = start + 2 + end + 2
	}
	return refs, nil
}

// IsSingleRef reports whether a template is exactly one reference, ignoring
// spaces around and inside it.
func IsSingleRef(template string) bool {
	trimmed := strings.TrimSpace(template)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return false
	}
	if strings.Count(trimmed, "{{") != 1 || strings.Count(trimmed, "}}") != 1 {
		return false
	}
	refs, err := Refs(trimmed)
	return err == nil && len(refs) == 1
}

// parseRef splits a reference's inner text at its first dot.
func parseRef(inner string) Ref {
	if i := strings.IndexByte(inner, '.'); i >= 0 {
		return Ref{Root: inner[:i], Attr: inner[i+1:]}
	}
	return Ref{Root: inner}
}

// snippet bounds a fragment for an error message.
func snippet(s string) string {
	const limit = 24
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
