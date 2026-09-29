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

// TestEnsureStateRootIsOwnerOnly pins that the root EnsureStateRoot creates is
// 0700. It compares against a same-umask control directory and skips when the
// umask leaves the control's owner bits clear, because then 0755 and 0700 are
// indistinguishable and the assertion could not fail.
func TestEnsureStateRootIsOwnerOnly(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "serve")

	control := filepath.Join(parent, "control")
	if err := os.Mkdir(control, 0o755); err != nil {
		t.Fatalf("mkdir control: %v", err)
	}
	controlInfo, err := os.Stat(control)
	if err != nil {
		t.Fatalf("stat control: %v", err)
	}
	if want := controlInfo.Mode().Perm() & 0o700; want != 0o700 {
		t.Skipf("umask masks the owner bits (control %o), so 0755 and 0700 are indistinguishable", controlInfo.Mode().Perm())
	}

	if err := EnsureStateRoot(root); err != nil {
		t.Fatalf("EnsureStateRoot: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("state root mode = %o, want 700", got)
	}
}
