package board

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// realDir returns path with symlinks resolved, so a test compares against the
// canonical root Resolve itself computes.
func realDir(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return real
}

func TestResolveDefault(t *testing.T) {
	root := realDir(t, t.TempDir())
	got, err := Resolve(root, root, "")
	if err != nil {
		t.Fatalf("Resolve default: %v", err)
	}
	want := filepath.Join(root, filepath.FromSlash(DefaultScene))
	if got != want {
		t.Errorf("Resolve default = %q, want %q", got, want)
	}
}

func TestResolveRelative(t *testing.T) {
	root := realDir(t, t.TempDir())
	got, err := Resolve(root, root, "docs/boards/notes.excalidraw")
	if err != nil {
		t.Fatalf("Resolve relative: %v", err)
	}
	want := filepath.Join(root, "docs", "boards", "notes.excalidraw")
	if got != want {
		t.Errorf("Resolve relative = %q, want %q", got, want)
	}
}

func TestResolveRefusesEscape(t *testing.T) {
	root := realDir(t, t.TempDir())
	_, err := Resolve(root, root, "../escape.excalidraw")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Resolve escape = %v, want ErrUsage", err)
	}
}

func TestResolveRefusesSymlinkedParentOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliable on windows")
	}
	root := realDir(t, t.TempDir())
	outside := realDir(t, t.TempDir())
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := Resolve(root, root, "link/scene.excalidraw")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Resolve through an escaping symlink = %v, want ErrUsage", err)
	}
}

func TestResolveRefusesWrongExtension(t *testing.T) {
	root := realDir(t, t.TempDir())
	_, err := Resolve(root, root, "notes.txt")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Resolve wrong extension = %v, want ErrUsage", err)
	}
}

func TestResolveAcceptsMissingFile(t *testing.T) {
	root := realDir(t, t.TempDir())
	got, err := Resolve(root, root, "docs/boards/new.excalidraw")
	if err != nil {
		t.Fatalf("Resolve missing file: %v", err)
	}
	if _, statErr := os.Stat(got); !os.IsNotExist(statErr) {
		t.Errorf("Resolve created %s: stat error = %v, want not-exist", got, statErr)
	}
}
