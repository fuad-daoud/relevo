package tmpdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempDirCreatesOwnerOnlyDir(t *testing.T) {
	root := t.TempDir()
	dir, err := TempDir(root)
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	want := filepath.Join(root, "tmp")
	if dir != want {
		t.Fatalf("TempDir = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if mode := info.Mode().Perm(); mode != StateRootMode {
		t.Errorf("mode = %o, want %o", mode, StateRootMode)
	}
}

func TestTempDirIsIdempotent(t *testing.T) {
	root := t.TempDir()
	first, err := TempDir(root)
	if err != nil {
		t.Fatalf("first TempDir: %v", err)
	}
	second, err := TempDir(root)
	if err != nil {
		t.Fatalf("second TempDir: %v", err)
	}
	if first != second {
		t.Errorf("TempDir not stable: %q then %q", first, second)
	}
}

func TestCreateTempUnderRoot(t *testing.T) {
	root := t.TempDir()
	f, err := CreateTemp(root, "relevo-test-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	defer func() { _ = os.Remove(name) }()

	if !strings.HasPrefix(name, filepath.Join(root, "tmp")+string(os.PathSeparator)) {
		t.Errorf("temp file %q is not under %s", name, filepath.Join(root, "tmp"))
	}
}

func TestMkdirTempUnderRoot(t *testing.T) {
	root := t.TempDir()
	dir, err := MkdirTemp(root, "relevo-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	if !strings.HasPrefix(dir, filepath.Join(root, "tmp")+string(os.PathSeparator)) {
		t.Errorf("temp dir %q is not under %s", dir, filepath.Join(root, "tmp"))
	}
}

func TestDefaultRootResolvesStateRoot(t *testing.T) {
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

func TestBlockedRootSurfacesError(t *testing.T) {
	// A file where the tmp dir must go makes MkdirAll fail, so the error
	// surfaces instead of silently falling back to /tmp.
	root := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(root, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	if _, err := CreateTemp(root, "x-*"); err == nil {
		t.Fatal("CreateTemp succeeded, want error")
	}
	if _, err := MkdirTemp(root, "x-*"); err == nil {
		t.Fatal("MkdirTemp succeeded, want error")
	}
}
