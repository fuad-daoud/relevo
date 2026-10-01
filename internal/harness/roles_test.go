package harness

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMissingDefinitions(t *testing.T) {
	role, ok := RoleByName("builder")
	if !ok {
		t.Fatal("builder role not found")
	}

	t.Run("one definition missing", func(t *testing.T) {
		env := freshEnv()
		env.files["/home/u/.config/opencode/agents/plan-executor.md"] = []byte("---\n---\n")
		got := MissingDefinitions(env, "opencode", role.Definitions)
		want := []string{".config/opencode/agents/researcher.md"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MissingDefinitions = %v, want %v", got, want)
		}
	})

	t.Run("both present", func(t *testing.T) {
		env := freshEnv()
		env.files["/home/u/.config/opencode/agents/plan-executor.md"] = []byte("---\n---\n")
		env.files["/home/u/.config/opencode/agents/researcher.md"] = []byte("---\n---\n")
		got := MissingDefinitions(env, "opencode", role.Definitions)
		if got != nil {
			t.Errorf("MissingDefinitions = %v, want nil", got)
		}
	})

	t.Run("unknown kind", func(t *testing.T) {
		env := freshEnv()
		got := MissingDefinitions(env, "nope", role.Definitions)
		if got != nil {
			t.Errorf("MissingDefinitions = %v, want nil", got)
		}
	})
}

func TestMissingDefinitionsCustomNames(t *testing.T) {
	t.Run("custom name missing", func(t *testing.T) {
		env := freshEnv()
		got := MissingDefinitions(env, "claude", []string{"my-executor"})
		want := []string{".claude/agents/my-executor.md"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MissingDefinitions = %v, want %v", got, want)
		}
	})

	t.Run("custom name present", func(t *testing.T) {
		env := freshEnv()
		env.files["/home/u/.claude/agents/my-executor.md"] = []byte("---\n---\n")
		got := MissingDefinitions(env, "claude", []string{"my-executor"})
		if got != nil {
			t.Errorf("MissingDefinitions = %v, want nil", got)
		}
	})

	t.Run("shipped name missing", func(t *testing.T) {
		env := freshEnv()
		got := MissingDefinitions(env, "claude", []string{"plan-executor"})
		want := []string{".claude/agents/plan-executor.md"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MissingDefinitions = %v, want %v", got, want)
		}
	})

	t.Run("unknown kind", func(t *testing.T) {
		env := freshEnv()
		got := MissingDefinitions(env, "nope", []string{"my-executor"})
		if got != nil {
			t.Errorf("MissingDefinitions = %v, want nil", got)
		}
	})
}

// TestMissingDefinitionsInAccountHome pins the per-account check: a definition
// present under the account home resolves at the account spelling
// (agents/<name>.md), and the missing path is reported in DefinitionPath's
// $HOME-relative spelling so a note renders it the usual way.
func TestMissingDefinitionsInAccountHome(t *testing.T) {
	home := "/homes/work"

	t.Run("present at the account spelling", func(t *testing.T) {
		env := freshEnv()
		env.files[filepath.Join(home, "agents", "plan-executor.md")] = []byte("---\n---\n")
		got := MissingDefinitionsIn(env, home, "claude", []string{"plan-executor"})
		if got != nil {
			t.Errorf("MissingDefinitionsIn = %v, want nil", got)
		}
	})

	t.Run("missing", func(t *testing.T) {
		env := freshEnv()
		got := MissingDefinitionsIn(env, home, "claude", []string{"plan-executor", "researcher"})
		want := []string{".claude/agents/plan-executor.md", ".claude/agents/researcher.md"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MissingDefinitionsIn = %v, want %v", got, want)
		}
	})

	t.Run("kind with no home", func(t *testing.T) {
		env := freshEnv()
		if got := MissingDefinitionsIn(env, "/homes/oc", "opencode", []string{"plan-executor"}); got != nil {
			t.Errorf("MissingDefinitionsIn(opencode) = %v, want nil", got)
		}
	})
}

// TestOSRoleCheckerMissingIn pins the checker availability's account path
// asserts: it answers MissingIn against a real home, and refuses a kind with no
// per-process home.
func TestOSRoleCheckerMissingIn(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "agents", "plan-executor.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := osRoleChecker{OSInstallEnv()}

	if got := c.MissingIn(home, "claude", []string{"plan-executor"}); got != nil {
		t.Errorf("MissingIn(present) = %v, want nil", got)
	}
	if got := c.MissingIn(home, "claude", []string{"reviewer"}); len(got) != 1 {
		t.Errorf("MissingIn(missing) = %v, want one path", got)
	}
	if got := c.MissingIn(home, "opencode", []string{"reviewer"}); got != nil {
		t.Errorf("MissingIn(opencode) = %v, want nil", got)
	}
}
