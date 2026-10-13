package relevo

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHookApplierRefusesUnsafeTargets pins the three rules the disk applier
// holds on its own, which no document-level test can reach: a target that is a
// symlink or a non-regular file is refused rather than followed, a write goes
// through a temp file and a rename in the target's own directory, and the
// executable bit is set only under this machine's hooks directory.
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
		if _, err := a.WriteHookFile(link, []byte("replaced\n"), true); err == nil {
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

	t.Run("the executable bit is set only under the hooks directory", func(t *testing.T) {
		t.Parallel()
		inside := filepath.Join(root, "hooks", "Stop.d", "run.sh")
		if _, err := a.WriteHookFile(inside, []byte("x\n"), true); err != nil {
			t.Fatalf("WriteHookFile under the hooks directory: %v", err)
		}
		if info, err := os.Stat(inside); err != nil || info.Mode()&0o111 == 0 {
			t.Errorf("mode under the hooks directory = %v, %v; want it executable", info, err)
		}
		outside := filepath.Join(t.TempDir(), "script.sh")
		if _, err := a.WriteHookFile(outside, []byte("x\n"), true); err != nil {
			t.Fatalf("WriteHookFile outside the hooks directory: %v", err)
		}
		info, err := os.Stat(outside)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode()&0o111 != 0 {
			t.Errorf("mode outside the hooks directory = %v, want no executable bit", info.Mode())
		}
	})

	t.Run("no temp file is left behind", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "hooks", "PreToolUse.d")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if _, err := a.WriteHookFile(filepath.Join(dir, "a.sh"), []byte("x\n"), true); err != nil {
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

// TestAgentApplierRefusesASymlinkedDefinition pins the agent half of the same
// rule: a definition path that is a symlink is neither read nor written
// through, so an arriving item cannot replace the file the link names.
func TestAgentApplierRefusesASymlinkedDefinition(t *testing.T) {
	t.Parallel()
	a := HookFileApplier(t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("do not touch\n"), 0o644); err != nil {
		t.Fatalf("write the outside file: %v", err)
	}
	link := filepath.Join(t.TempDir(), "agents", "linked.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := a.ReadAgentFile(link); err == nil {
		t.Error("ReadAgentFile followed the symlink; want a refusal")
	}
	if _, err := a.WriteAgentFile(link, []byte("replaced\n")); err == nil {
		t.Error("WriteAgentFile followed the symlink; want a refusal")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "do not touch\n" {
		t.Errorf("the file the link named = %q, %v; want it untouched", data, err)
	}
}
