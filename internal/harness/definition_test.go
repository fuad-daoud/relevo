package harness

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/account"
)

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

// TestAccountDefinitionPath pins the account-home spelling: the home variable
// replaces the ~/.claude or ~/.codex component, so the path is
// DefinitionPath with that component stripped. A kind with no per-process home
// has no account path.
func TestAccountDefinitionPath(t *testing.T) {
	tests := []struct {
		kind, name string
		want       string
		ok         bool
	}{
		{"claude", "plan-executor", "agents/plan-executor.md", true},
		{"codex", "researcher", "researcher.config.toml", true},
		{"claude", "my-executor", "agents/my-executor.md", true},
		{"codex", "my-executor", "my-executor.config.toml", true},
		{"opencode", "reviewer", "", false},
		{"agy", "reviewer", "", false},
		{"nope", "reviewer", "", false},
	}
	for _, tt := range tests {
		got, ok := AccountDefinitionPath(tt.kind, tt.name)
		if ok != tt.ok || got != tt.want {
			t.Errorf("AccountDefinitionPath(%q, %q) = %q, %v; want %q, %v", tt.kind, tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// TestHomeEnv pins which kinds have a per-process home and which variable names
// it: the set an account can be pinned for.
func TestHomeEnv(t *testing.T) {
	if name, ok := HomeEnv("claude"); !ok || name != "CLAUDE_CONFIG_DIR" {
		t.Errorf("HomeEnv(claude) = %q, %v; want CLAUDE_CONFIG_DIR, true", name, ok)
	}
	if name, ok := HomeEnv("codex"); !ok || name != "CODEX_HOME" {
		t.Errorf("HomeEnv(codex) = %q, %v; want CODEX_HOME, true", name, ok)
	}
	for _, kind := range []string{"opencode", "agy", "nope"} {
		if name, ok := HomeEnv(kind); ok || name != "" {
			t.Errorf("HomeEnv(%q) = %q, %v; want \"\", false", kind, name, ok)
		}
	}
}

// TestAccountHome pins the directory an account pins its harness to, and the
// kinds and empty selectors that have none.
func TestAccountHome(t *testing.T) {
	tests := []struct {
		name string
		a    account.Account
		want string
		ok   bool
	}{
		{"claude", account.Account{Harness: account.Claude, ConfigDir: "/homes/work"}, "/homes/work", true},
		{"codex", account.Account{Harness: account.Codex, Home: "/homes/codex"}, "/homes/codex", true},
		{"claude empty", account.Account{Harness: account.Claude}, "", false},
		{"codex empty", account.Account{Harness: account.Codex}, "", false},
		{"opencode", account.Account{Harness: account.OpenCode, Integration: "x", Label: "y"}, "", false},
	}
	for _, tt := range tests {
		got, ok := AccountHome(tt.a)
		if got != tt.want || ok != tt.ok {
			t.Errorf("AccountHome(%s) = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}
