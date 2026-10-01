// Package pathscope judges the paths a writer actor's round changed against
// the actor's declared scope (#801 slice 2).
//
// A scope is a list of globs over repo-relative paths plus, for Go files,
// whether a comment-only edit is allowed. Scope itself is pure; Judge walks a
// git-reported change list and returns the paths it refuses.
package pathscope

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Scope is a writer actor's declared scope: the path patterns a round of it
// may change, and whether a change confined to Go comments is allowed.
type Scope struct {
	// Paths are globs over repo-relative paths, evaluated in order: a
	// leading "!" excludes, and the last matching entry wins. An entry may
	// name a built-in set as "@docs" instead of a glob.
	Paths []string `json:"paths"`
	// Comments allows a Go file's modification when the old and new contents
	// differ only in non-directive comments.
	Comments bool `json:"comments"`
}

// DocsSet is what "@docs" expands to: the documentation-ish file types, with
// the noise directories excluded. It is a var so a test can read it; callers
// treat it as read-only.
var DocsSet = []string{
	"**/*.md",
	"**/*.markdown",
	"**/*.mdx",
	"**/*.mmd",
	"**/*.svg",
	"**/*.excalidraw",
	"!**/testdata/**",
	"!vendor/**",
	"!**/node_modules/**",
}

// sets names every built-in @set. A name not here is a validation error.
var sets = map[string][]string{
	"@docs": DocsSet,
}

// ErrUnknownSet reports a "@name" path an entry names that no built-in set
// defines.
var ErrUnknownSet = errors.New("unknown @set")

// Validate applies the scope's own rules: every path is a non-empty glob, or
// a "@set" this package knows. It says nothing about the actor's shape; the
// reader refusal lives where shape is known.
func (s Scope) Validate() error {
	for i, p := range s.Paths {
		if p == "" {
			return fmt.Errorf("paths[%d]: empty pattern", i)
		}
		if strings.HasPrefix(p, "@") {
			if _, ok := sets[p]; !ok {
				return fmt.Errorf("paths[%d]: %q: %w", i, p, ErrUnknownSet)
			}
			continue
		}
		if _, _, err := compilePattern(p); err != nil {
			return fmt.Errorf("paths[%d]: %w", i, err)
		}
	}
	return nil
}

// Match reports whether path is in scope: the patterns are applied in order,
// each matching entry setting the verdict to in (a plain entry) or out (a
// "!" entry), so the last match wins. A "@set" entry contributes that set's
// own patterns at its position.
func (s Scope) Match(path string) bool {
	matched := false
	for _, p := range s.Paths {
		entries := []string{p}
		if set, ok := sets[p]; ok {
			entries = set
		}
		for _, e := range entries {
			re, neg, err := compilePattern(e)
			if err != nil {
				continue
			}
			if re.MatchString(path) {
				matched = !neg
			}
		}
	}
	return matched
}

// compilePattern compiles one entry to an anchored regexp, reporting whether
// it is a "!" exclusion.
func compilePattern(entry string) (*regexp.Regexp, bool, error) {
	neg := false
	pat := entry
	if strings.HasPrefix(pat, "!") {
		neg = true
		pat = pat[1:]
	}
	if pat == "" {
		return nil, neg, errors.New("empty pattern")
	}
	re, err := regexp.Compile("^" + globToRegexp(pat) + "$")
	if err != nil {
		return nil, neg, err
	}
	return re, neg, nil
}

// globToRegexp translates a scope glob to a regexp body: "*" matches within
// one path segment, "**" any number of segments, and "**/" also matches zero
// segments so "**/*.md" matches a root-level "README.md".
func globToRegexp(pat string) string {
	var b strings.Builder
	for i := 0; i < len(pat); {
		if pat[i] == '*' {
			if i+1 < len(pat) && pat[i+1] == '*' {
				if i+2 < len(pat) && pat[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 3
				} else {
					b.WriteString(".*")
					i += 2
				}
				continue
			}
			b.WriteString("[^/]*")
			i++
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(pat[i])))
		i++
	}
	return b.String()
}
