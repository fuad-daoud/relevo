package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

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
	if _, err := tmpdir.CreateTemp(root, "x-*"); err == nil {
		t.Fatal("CreateTemp succeeded, want error")
	}
	if _, err := tmpdir.MkdirTemp(root, "x-*"); err == nil {
		t.Fatal("MkdirTemp succeeded, want error")
	}
}

func TestStoreRootNilStore(t *testing.T) {
	if got := StoreRoot(nil); got != "" {
		t.Errorf("StoreRoot(nil) = %q, want empty", got)
	}
}
