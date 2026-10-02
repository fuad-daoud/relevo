package store

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

func TestWriteTempWritesData(t *testing.T) {
	root := t.TempDir()
	name, err := WriteTemp(root, "relevo-test-*.md", []byte("# plan\n"))
	if err != nil {
		t.Fatalf("WriteTemp: %v", err)
	}
	defer func() { _ = os.Remove(name) }()

	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "# plan\n" {
		t.Errorf("content = %q, want %q", got, "# plan\n")
	}
}

func TestWriteTempReportsRootError(t *testing.T) {
	// A file where the tmp dir must go makes MkdirAll fail, so the error
	// surfaces instead of silently falling back to /tmp.
	root := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(root, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	if _, err := WriteTemp(root, "x-*.md", []byte("d")); err == nil {
		t.Fatal("WriteTemp succeeded, want error")
	}
	if _, err := CreateTemp(root, "x-*"); err == nil {
		t.Fatal("CreateTemp succeeded, want error")
	}
	if _, err := MkdirTemp(root, "x-*"); err == nil {
		t.Fatal("MkdirTemp succeeded, want error")
	}
}

func TestStoreRootNilStore(t *testing.T) {
	if got := StoreRoot(nil); got != "" {
		t.Errorf("StoreRoot(nil) = %q, want empty", got)
	}
}
