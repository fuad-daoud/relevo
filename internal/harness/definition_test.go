package harness

import "testing"

func TestDefinitionPathShippedRows(t *testing.T) {
	for _, h := range All() {
		if len(h.Roles) == 0 {
			t.Errorf("harness %q ships no roles", h.Kind)
		}
		for _, r := range h.Roles {
			got, ok := DefinitionPath(h.Kind, r.Name)
			if !ok {
				t.Errorf("DefinitionPath(%q, %q) ok = false, want true", h.Kind, r.Name)
				continue
			}
			if got != r.Path {
				t.Errorf("DefinitionPath(%q, %q) = %q, want %q", h.Kind, r.Name, got, r.Path)
			}
		}
	}
}

func TestDefinitionPathConvention(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"claude", ".claude/agents/my-executor.md"},
		{"opencode", ".config/opencode/agents/my-executor.md"},
		{"agy", ".gemini/config/agents/my-executor.md"},
		{"codex", ".codex/my-executor.config.toml"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			got, ok := DefinitionPath(tt.kind, "my-executor")
			if !ok {
				t.Fatalf("DefinitionPath(%q, %q) ok = false, want true", tt.kind, "my-executor")
			}
			if got != tt.want {
				t.Errorf("DefinitionPath(%q, %q) = %q, want %q", tt.kind, "my-executor", got, tt.want)
			}
		})
	}
}

func TestDefinitionPathUnknownKind(t *testing.T) {
	got, ok := DefinitionPath("nope", "reviewer")
	if ok {
		t.Errorf("DefinitionPath(%q, %q) ok = true, want false", "nope", "reviewer")
	}
	if got != "" {
		t.Errorf("DefinitionPath(%q, %q) = %q, want \"\"", "nope", "reviewer", got)
	}
}

func TestIsShipped(t *testing.T) {
	tests := []struct {
		kind, name string
		want       bool
	}{
		{"claude", "architect", true},
		{"claude", "plan-executor", true},
		{"codex", "researcher", true},
		{"claude", "my-executor", false},
		{"nope", "reviewer", false},
	}

	for _, tt := range tests {
		if got := IsShipped(tt.kind, tt.name); got != tt.want {
			t.Errorf("IsShipped(%q, %q) = %v, want %v", tt.kind, tt.name, got, tt.want)
		}
	}
}
