package serve

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureStateRoot pins the helper's contract (three arms): it creates
// <root>/bindings (and the root) from nothing, a second call is nil and
// leaves a sentinel file alone, and a regular file at <root>/bindings is an
// error rather than a silent success.
func TestEnsureStateRoot(t *testing.T) {
	t.Run("creates bindings and root from nothing", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "serve")

		if err := EnsureStateRoot(root); err != nil {
			t.Fatalf("EnsureStateRoot: %v", err)
		}
		info, err := os.Stat(filepath.Join(root, "bindings"))
		if err != nil {
			t.Fatalf("stat <root>/bindings: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("<root>/bindings is not a directory")
		}
	})

	t.Run("second call is nil and leaves a sentinel alone", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "serve")
		if err := EnsureStateRoot(root); err != nil {
			t.Fatalf("first EnsureStateRoot: %v", err)
		}

		sentinel := filepath.Join(root, "bindings", "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep me\n"), 0o644); err != nil {
			t.Fatalf("write sentinel: %v", err)
		}

		if err := EnsureStateRoot(root); err != nil {
			t.Fatalf("second EnsureStateRoot: %v", err)
		}
		got, err := os.ReadFile(sentinel)
		if err != nil {
			t.Fatalf("read sentinel: %v", err)
		}
		if string(got) != "keep me\n" {
			t.Errorf("sentinel = %q, want it untouched", got)
		}
	})

	t.Run("a file at bindings is an error", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "serve")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "bindings"), []byte("not a dir\n"), 0o644); err != nil {
			t.Fatalf("write file at bindings: %v", err)
		}

		if err := EnsureStateRoot(root); err == nil {
			t.Fatal("EnsureStateRoot over a file at bindings = nil, want an error")
		}
	})
}
