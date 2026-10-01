package pathscope

import "testing"

// TestDocsSetMatchesKnownFiles pins @docs's shape: a markdown file anywhere is
// in, including a root-level one, while a testdata copy and a docs probe
// script are out.
func TestDocsSetMatchesKnownFiles(t *testing.T) {
	t.Parallel()

	scope := Scope{Paths: []string{"@docs"}}
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"README.md", true},                               // ** matches zero directories
		{"docs/design.md", true},                          // nested
		{"a/b/c.markdown", true},                          // another extension
		{"docs/diagram.svg", true},                        // a root-level svg would too
		{"internal/foo/testdata/x.md", false},             // !**/testdata/**
		{"testdata/x.md", false},                          // testdata at the root
		{"vendor/github.com/x/y/z.md", false},             // !vendor/**
		{"a/node_modules/b.md", false},                    // !**/node_modules/**
		{"docs/specs/probes/x.ts", false},                 // .ts is not a docs type
		{"internal/relevo/reconcile.go", false},           // code is not a docs type
		{"internal/harness/agents/shipped.sha256", false}, // no extension
	} {
		if got := scope.Match(tt.path); got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// TestScopeLastMatchWins pins the "! excludes, last match wins" rule: a later
// entry re-includes a path an earlier exclusion took out, and vice versa.
func TestScopeLastMatchWins(t *testing.T) {
	t.Parallel()

	excludeLast := Scope{Paths: []string{"docs/**", "!docs/draft.md"}}
	if !excludeLast.Match("docs/a.md") {
		t.Error("docs/a.md should be in scope via docs/**")
	}
	if excludeLast.Match("docs/draft.md") {
		t.Error("docs/draft.md should be excluded by the later ! entry")
	}

	includeLast := Scope{Paths: []string{"!docs/**", "docs/keep.md"}}
	if includeLast.Match("docs/keep.md") {
		// The later plain entry wins, so it is in scope.
	} else {
		t.Error("docs/keep.md should be re-included by the later entry")
	}
	if includeLast.Match("docs/other.md") {
		t.Error("docs/other.md should stay excluded")
	}
}

// TestScopeValidateUnknownSet pins that an unknown @name is refused, while
// @docs and a plain glob are accepted.
func TestScopeValidateUnknownSet(t *testing.T) {
	t.Parallel()

	if err := (Scope{Paths: []string{"@docs"}, Comments: true}).Validate(); err != nil {
		t.Errorf("Validate(@docs) = %v, want nil", err)
	}
	if err := (Scope{Paths: []string{"docs/**", "!docs/old/**"}}).Validate(); err != nil {
		t.Errorf("Validate(globs) = %v, want nil", err)
	}
	err := (Scope{Paths: []string{"@nope"}}).Validate()
	if err == nil {
		t.Fatal("Validate(@nope) = nil, want an error")
	}
}

// TestScopeValidateEmptyPattern pins that an empty entry is refused.
func TestScopeValidateEmptyPattern(t *testing.T) {
	t.Parallel()

	if err := (Scope{Paths: []string{""}}).Validate(); err == nil {
		t.Fatal("Validate(\"\") = nil, want an error")
	}
}
