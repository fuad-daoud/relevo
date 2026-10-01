package pathscope

import "testing"

// blobs is a readBlob over a fixed old/new pair, keyed by object id.
func blobs(m map[string]string) func(string) ([]byte, error) {
	return func(oid string) ([]byte, error) {
		return []byte(m[oid]), nil
	}
}

// TestJudgeRefusesSymlinkAndModeChange is case 1: a symlink or gitlink on
// either side, or a file-mode change between two present sides.
func TestJudgeRefusesSymlinkAndModeChange(t *testing.T) {
	t.Parallel()

	read := blobs(nil)
	for _, tt := range []struct {
		name   string
		change Change
	}{
		{"symlink old", Change{Status: 'M', OldMode: "120000", NewMode: "100644", Path: "x"}},
		{"symlink new", Change{Status: 'M', OldMode: "100644", NewMode: "120000", Path: "x"}},
		{"gitlink old", Change{Status: 'M', OldMode: "160000", NewMode: "160000", Path: "sub"}},
		{"mode change", Change{Status: 'M', OldMode: "100644", NewMode: "100755", Path: "script.sh"}},
	} {
		v := Judge(Scope{Paths: []string{"**"}}, []Change{tt.change}, read)
		if len(v) != 1 {
			t.Fatalf("%s: violations = %v, want one", tt.name, v)
		}
		if v[0].Path != tt.change.Path {
			t.Errorf("%s: path = %q, want %q", tt.name, v[0].Path, tt.change.Path)
		}
	}
}

// TestJudgePathInScope is case 2: a path the patterns name is in scope,
// whatever the status.
func TestJudgePathInScope(t *testing.T) {
	t.Parallel()

	scope := Scope{Paths: []string{"docs/**"}}
	for _, c := range []Change{
		{Status: 'A', Path: "docs/new.md"},
		{Status: 'M', Path: "docs/old.md"},
		{Status: 'D', Path: "docs/gone.md"},
		{Status: 'M', Path: "docs/code.go"}, // in scope by path, not by comments
	} {
		if v := Judge(scope, []Change{c}, blobs(nil)); len(v) != 0 {
			t.Errorf("in-scope %s %q refused: %v", string(c.Status), c.Path, v)
		}
	}
}

// TestJudgeCommentOnlyPasses is case 3: an M .go file whose old and new
// contents differ only in non-directive comments.
func TestJudgeCommentOnlyPasses(t *testing.T) {
	t.Parallel()

	read := blobs(map[string]string{
		"old": "package p\n\n// old wording\nfunc F() {}\n",
		"new": "package p\n\n// new wording\nfunc F() {}\n",
	})
	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", OldOID: "old", NewOID: "new", Path: "p/f.go"}
	if v := Judge(Scope{Comments: true}, []Change{c}, read); len(v) != 0 {
		t.Fatalf("comment-only edit refused: %v", v)
	}
}

// TestJudgeCodeChangeRefused is case 4: an M .go file that differs in a
// non-comment token.
func TestJudgeCodeChangeRefused(t *testing.T) {
	t.Parallel()

	read := blobs(map[string]string{
		"old": "package p\n\nfunc F() { x := 1; _ = x }\n",
		"new": "package p\n\nfunc F() { x := 2; _ = x }\n",
	})
	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", OldOID: "old", NewOID: "new", Path: "p/f.go"}
	v := Judge(Scope{Comments: true}, []Change{c}, read)
	if len(v) != 1 || v[0].Reason != ReasonCodeChange {
		t.Fatalf("violations = %v, want one %q", v, ReasonCodeChange)
	}
}

// TestJudgeDirectiveChangeRefused is case 5: an M .go file whose non-comment
// tokens are equal but whose directive comments differ.
func TestJudgeDirectiveChangeRefused(t *testing.T) {
	t.Parallel()

	read := blobs(map[string]string{
		"old": "//go:build linux\n\npackage p\n",
		"new": "//go:build darwin\n\npackage p\n",
	})
	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", OldOID: "old", NewOID: "new", Path: "p/f.go"}
	v := Judge(Scope{Comments: true}, []Change{c}, read)
	if len(v) != 1 || v[0].Reason != ReasonDirectiveComment {
		t.Fatalf("violations = %v, want one %q", v, ReasonDirectiveComment)
	}
}

// TestJudgeCannotJudge is case 6: an M .go file that does not scan cleanly on
// either side, or imports "C".
func TestJudgeCannotJudge(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		old  string
		new  string
	}{
		{"old does not scan", "package p\nfunc (\n", "package p\n"},
		{"new does not scan", "package p\n", "package p\nfunc (\n"},
		{"cgo", "package p\n\n// #include <stdio.h>\nimport \"C\"\n", "package p\n\nimport \"C\"\n"},
	} {
		read := blobs(map[string]string{"old": tt.old, "new": tt.new})
		c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", OldOID: "old", NewOID: "new", Path: "p/f.go"}
		v := Judge(Scope{Comments: true}, []Change{c}, read)
		if len(v) != 1 || v[0].Reason != ReasonCannotJudge {
			t.Errorf("%s: violations = %v, want one %q", tt.name, v, ReasonCannotJudge)
		}
	}
}

// TestJudgeNewOrDeletedGoFile is case 7: a .go file added or deleted.
func TestJudgeNewOrDeletedGoFile(t *testing.T) {
	t.Parallel()

	for _, status := range []byte{'A', 'D'} {
		oldMode, newMode := "000000", "100644"
		if status == 'D' {
			oldMode, newMode = "100644", "000000"
		}
		c := Change{Status: status, OldMode: oldMode, NewMode: newMode, Path: "p/new.go"}
		v := Judge(Scope{Comments: true}, []Change{c}, blobs(nil))
		if len(v) != 1 || v[0].Reason != ReasonNewOrDeletedSource {
			t.Errorf("status %s: violations = %v, want one %q", string(status), v, ReasonNewOrDeletedSource)
		}
	}
}

// TestJudgeOtherExtensionRefused is case 8: a non-Go path outside the scope's
// patterns.
func TestJudgeOtherExtensionRefused(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"config.json", "internal/git/client.go.txt", "docs/specs/probes/x.ts"} {
		c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", Path: path}
		v := Judge(Scope{Comments: true}, []Change{c}, blobs(nil))
		if len(v) != 1 || v[0].Reason != ReasonOutsideScope {
			t.Errorf("%q: violations = %v, want one %q", path, v, ReasonOutsideScope)
		}
	}
}

// TestJudgeCommentsDisallowedGoRefused pins that an out-of-scope M .go file
// is refused when the scope does not allow comment judgement.
func TestJudgeCommentsDisallowedGoRefused(t *testing.T) {
	t.Parallel()

	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", Path: "p/f.go"}
	v := Judge(Scope{Comments: false}, []Change{c}, blobs(nil))
	if len(v) != 1 {
		t.Fatalf("violations = %v, want one", v)
	}
}
