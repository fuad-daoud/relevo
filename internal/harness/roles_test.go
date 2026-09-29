package harness

import (
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
