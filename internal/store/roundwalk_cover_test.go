package store

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestWalkArtifactDir pins the artifact walk: every file below an NNN-<actor>
// directory is named "NNN-<actor>/<rel>" with forward slashes, while a symlink
// and a dot entry are skipped and an unreadable directory is an error.
func TestWalkArtifactDir(t *testing.T) {
	dir := t.TempDir()
	art := filepath.Join(dir, "003-builder")
	if err := os.MkdirAll(filepath.Join(art, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "findings.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "sub", "deep.md"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, ".dotfile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(art, "link")); err != nil {
		t.Fatal(err)
	}

	var got []string
	err := walkArtifactDir(art, "003-builder", 3, func(f diskRoundFile) { got = append(got, f.name) })
	if err != nil {
		t.Fatalf("walkArtifactDir: %v", err)
	}
	sort.Strings(got)
	want := []string{"003-builder/findings.md", "003-builder/sub/deep.md"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("walkArtifactDir names = %v, want %v (dot entry and symlink skipped)", got, want)
	}

	if err := walkArtifactDir(filepath.Join(dir, "absent"), "003-builder", 3, func(diskRoundFile) {}); err == nil {
		t.Error("walkArtifactDir(absent) = nil error, want a refusal")
	}
}

// TestRemoveEmptyDirsBelow pins the bottom-up prune: an empty directory is
// removed while one holding a file is kept, and a symlink is never followed.
func TestRemoveEmptyDirsBelow(t *testing.T) {
	base := t.TempDir()
	empty := filepath.Join(base, "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(base, "full")
	if err := os.MkdirAll(filepath.Join(full, "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "keep.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "linkdir")); err != nil {
		t.Fatal(err)
	}

	removeEmptyDirsBelow(base)

	if _, err := os.Lstat(empty); !os.IsNotExist(err) {
		t.Errorf("empty directory still present (err %v), want it removed", err)
	}
	if _, err := os.Stat(filepath.Join(full, "keep.md")); err != nil {
		t.Errorf("non-empty directory's file = %v, want it kept", err)
	}
	if fi, err := os.Lstat(filepath.Join(base, "linkdir")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink = (%v, %v), want it left untouched", fi, err)
	}
}
