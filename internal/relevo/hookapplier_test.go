package relevo

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHookApplierRefusesUnsafeTargets pins the three rules the disk applier
// holds on its own, which no document-level test can reach: a target that is a
// symlink or a non-regular file is refused rather than followed, a write goes
// through a temp file and a rename in the target's own directory, and a synced
// script never arrives executable.
func TestHookApplierRefusesUnsafeTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := HookFileApplier(root)

	t.Run("the directory is computed from the names", func(t *testing.T) {
		t.Parallel()
		got, err := a.HookFilePath("PreToolUse", "check.sh")
		if err != nil {
			t.Fatalf("HookFilePath: %v", err)
		}
		want := filepath.Join(root, "hooks", "PreToolUse.d", "check.sh")
		if got != want {
			t.Errorf("HookFilePath = %q, want %q", got, want)
		}
	})

	t.Run("an unsafe name is refused", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"../escape.sh", "a/b", ".", ".."} {
			if _, err := a.HookFilePath("PreToolUse", name); err == nil {
				t.Errorf("HookFilePath(%q) accepted the name; want a refusal", name)
			}
		}
	})

	t.Run("a symlinked target is refused, not followed", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(root, "hooks", "PreToolUse.d")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		outside := filepath.Join(t.TempDir(), "outside.sh")
		if err := os.WriteFile(outside, []byte("do not touch\n"), 0o755); err != nil {
			t.Fatalf("write the outside file: %v", err)
		}
		link := filepath.Join(dir, "link.sh")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := a.WriteHookFile(link, []byte("replaced\n")); err == nil {
			t.Error("WriteHookFile followed the symlink; want a refusal")
		}
		data, err := os.ReadFile(outside)
		if err != nil {
			t.Fatalf("read the outside file: %v", err)
		}
		if string(data) != "do not touch\n" {
			t.Errorf("the file the link named = %q, want it untouched", data)
		}
	})

	t.Run("a new file lands 0644 whatever the sender had", func(t *testing.T) {
		t.Parallel()
		target := filepath.Join(t.TempDir(), "hooks", "PreToolUse.d", "script.sh")
		if _, err := a.WriteHookFile(target, []byte("x\n")); err != nil {
			t.Fatalf("WriteHookFile: %v", err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v, want 0644", info.Mode().Perm())
		}
	})

	t.Run("a rewrite keeps a local chmod", func(t *testing.T) {
		t.Parallel()
		target := filepath.Join(t.TempDir(), "script.sh")
		if err := os.WriteFile(target, []byte("old\n"), 0o755); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := a.WriteHookFile(target, []byte("new\n")); err != nil {
			t.Fatalf("WriteHookFile: %v", err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode = %v, want the local 0755 kept", info.Mode().Perm())
		}
	})

	t.Run("no temp file is left behind", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "hooks", "PreToolUse.d")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if _, err := a.WriteHookFile(filepath.Join(dir, "a.sh"), []byte("x\n")); err != nil {
			t.Fatalf("WriteHookFile: %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "a.sh" {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("directory holds %v, want only the target", names)
		}
	})
}
