package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelWithin pins the lexical containment rule: a path inside dir resolves
// to its name, and the directory itself, a sibling sharing dir's prefix, and a
// path outside dir are all refused.
func TestRelWithin(t *testing.T) {
	cases := []struct {
		dir, path, want string
		ok              bool
	}{
		{dir: "/a/b", path: "/a/b/c", want: "c", ok: true},
		{dir: "/a/b", path: "/a/b/c/d", want: "c/d", ok: true},
		{dir: "/a/b", path: "/a/b"},
		{dir: "/a/b", path: "/a"},
		{dir: "/a/b", path: "/a/bc"},
		{dir: "/a/b", path: "/a/c"},
	}
	for _, tc := range cases {
		got, ok := relWithin(tc.dir, tc.path)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("relWithin(%q, %q) = (%q, %v), want (%q, %v)", tc.dir, tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

// rootFixture builds a directory holding a regular file, a directory and a
// symlink to the regular file, and opens it as a confined root.
func rootFixture(t *testing.T) *os.Root {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "f"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestRootReadRegularFile pins rootReadRegularFile: a regular file reads, a
// symlink and a directory are refused, and an absent name is (absent, no
// error).
func TestRootReadRegularFile(t *testing.T) {
	root := rootFixture(t)

	body, ok, err := rootReadRegularFile(root, "f")
	if err != nil || !ok || string(body) != "data" {
		t.Fatalf("rootReadRegularFile = (%q, %v, %v), want (data, true, nil)", body, ok, err)
	}
	if _, ok, err := rootReadRegularFile(root, "nope"); ok || err != nil {
		t.Errorf("rootReadRegularFile(absent) = (_, %v, %v), want (false, nil)", ok, err)
	}
	if _, ok, err := rootReadRegularFile(root, "link"); ok || err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("rootReadRegularFile(symlink) = (_, %v, %v), want a refusal", ok, err)
	}
	if _, ok, err := rootReadRegularFile(root, "d"); ok || err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("rootReadRegularFile(directory) = (_, %v, %v), want a refusal", ok, err)
	}
}

// TestRootStatRegularFile pins rootStatRegularFile: it reports a regular file
// and refuses a symlink, with an absent name reported as (absent, no error).
func TestRootStatRegularFile(t *testing.T) {
	root := rootFixture(t)

	fi, ok, err := rootStatRegularFile(root, "f")
	if err != nil || !ok || fi == nil || !fi.Mode().IsRegular() {
		t.Fatalf("rootStatRegularFile(regular) = (%v, %v, %v), want a regular file", fi, ok, err)
	}
	if fi, ok, err := rootStatRegularFile(root, "link"); ok || err == nil || fi != nil {
		t.Errorf("rootStatRegularFile(symlink) = (%v, %v, %v), want a refusal", fi, ok, err)
	}
	if _, ok, err := rootStatRegularFile(root, "nope"); ok || err != nil {
		t.Errorf("rootStatRegularFile(absent) = (_, %v, %v), want (false, nil)", ok, err)
	}
}

// TestRootReadDir pins rootReadDir: a directory lists, an absent name is
// (nil, no error), and a file is refused.
func TestRootReadDir(t *testing.T) {
	root := rootFixture(t)

	entries, err := rootReadDir(root, "d")
	if err != nil || entries == nil {
		t.Errorf("rootReadDir(directory) = (%v, %v), want an empty, non-nil listing", entries, err)
	}
	if entries, err := rootReadDir(root, "nope"); entries != nil || err != nil {
		t.Errorf("rootReadDir(absent) = (%v, %v), want (nil, nil)", entries, err)
	}
	if _, err := rootReadDir(root, "f"); err == nil {
		t.Error("rootReadDir(file) = nil error, want a refusal")
	}
}

// TestOutRootAndWorktreeRoot pins that a binding's out/ and .worktrees/ open as
// confined roots when they exist, and report an error when they do not.
func TestOutRootAndWorktreeRoot(t *testing.T) {
	s := New(t.TempDir())
	if err := os.MkdirAll(s.OutDir("webshop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.WorktreeDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	out, err := s.OutRoot("webshop")
	if err != nil {
		t.Fatalf("OutRoot: %v", err)
	}
	_ = out.Close()

	wt, err := s.WorktreeRoot()
	if err != nil {
		t.Fatalf("WorktreeRoot: %v", err)
	}
	_ = wt.Close()

	if r, err := s.OutRoot("absent"); err == nil {
		_ = r.Close()
		t.Error("OutRoot(absent) = nil error, want a refusal")
	}
}

// TestPathBuilders pins the new path helpers and DefaultRoot's two homes.
func TestPathBuilders(t *testing.T) {
	s := New("/root")
	if got := s.Owner(); got != "" {
		t.Errorf("Owner = %q, want \"\"", got)
	}

	if got := ArtifactRel(3, "builder", "sub/f.md"); got != "003-builder/sub/f.md" {
		t.Errorf("ArtifactRel = %q, want 003-builder/sub/f.md", got)
	}
	if got := s.GateLogPath("webshop", 3); !strings.HasSuffix(got, "003-gate.log") {
		t.Errorf("GateLogPath = %q, want it to end in 003-gate.log", got)
	}
	if got := s.MasterMindsDir(); got != filepath.Join("/root", "planners") {
		t.Errorf("MasterMindsDir = %q, want /root/planners", got)
	}
	if got := s.AgyCredsDir(); got != filepath.Join("/root", "planners", ".agy") {
		t.Errorf("AgyCredsDir = %q, want /root/planners/.agy", got)
	}
	if got := s.VerifyWorktreePath("webshop", 3); got != filepath.Join("/root", ".worktrees", ".verify", "webshop-003") {
		t.Errorf("VerifyWorktreePath = %q", got)
	}
	if got := s.ScratchWorktreeDir(); got != filepath.Join("/root", ".worktrees", ".scratch") {
		t.Errorf("ScratchWorktreeDir = %q", got)
	}
	if got := s.ScratchWorktreePath("webshop", 3); got != filepath.Join("/root", ".worktrees", ".scratch", "webshop-003") {
		t.Errorf("ScratchWorktreePath = %q", got)
	}
	if got := s.WorktreePath("webshop"); got != filepath.Join("/root", ".worktrees", "webshop") {
		t.Errorf("WorktreePath = %q", got)
	}

	t.Run("XDG_STATE_HOME wins", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "/xdg")
		got, err := DefaultRoot()
		if err != nil || got != filepath.Join("/xdg", "relevo") {
			t.Errorf("DefaultRoot = (%q, %v), want /xdg/relevo", got, err)
		}
	})
	t.Run("falls back to home", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "/home/u")
		got, err := DefaultRoot()
		if err != nil || got != filepath.Join("/home/u", ".local", "state", "relevo") {
			t.Errorf("DefaultRoot = (%q, %v), want the home fallback", got, err)
		}
	})
}
