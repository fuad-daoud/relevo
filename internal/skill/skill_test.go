package skill

import (
	"reflect"
	"testing"
)

func TestLocationsPerKind(t *testing.T) {
	tests := []struct {
		kind string
		want []string
	}{
		{"claude", []string{"/r/.claude/skills/s/SKILL.md", "/h/.claude/skills/s/SKILL.md"}},
		{"opencode", []string{"/r/.agents/skills/s/SKILL.md", "/h/.agents/skills/s/SKILL.md"}},
		{"agy", []string{"/r/.agents/skills/s/SKILL.md", "/h/.agents/skills/s/SKILL.md"}},
		{"codex", []string{"/r/.agents/skills/s/SKILL.md", "/h/.agents/skills/s/SKILL.md", "/h/.codex/skills/s/SKILL.md"}},
		{"muse", []string{"/r/.agents/skills/s/SKILL.md", "/r/.claude/skills/s/SKILL.md", "/h/.agents/skills/s/SKILL.md", "/h/.claude/skills/s/SKILL.md"}},
		{"nope", nil},
	}
	for _, tt := range tests {
		if got := Locations(tt.kind, "s", "/r", "/h"); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestServable(t *testing.T) {
	for _, k := range []string{"claude", "opencode", "agy", "codex", "muse"} {
		if !Servable(k) {
			t.Errorf("%s should be servable", k)
		}
	}
	if Servable("nope") {
		t.Error("unknown kind must not be servable")
	}
}

func TestEmptyRepoSkipsProjectPaths(t *testing.T) {
	got := Locations("claude", "s", "", "/h")
	if want := []string{"/h/.claude/skills/s/SKILL.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolve(t *testing.T) {
	has := func(set ...string) func(string) bool {
		return func(p string) bool {
			for _, s := range set {
				if s == p {
					return true
				}
			}
			return false
		}
	}
	both := has("/r/.claude/skills/s/SKILL.md", "/h/.claude/skills/s/SKILL.md")
	if got, _ := Resolve(both, "claude", "s", "/r", "/h"); got != "/r/.claude/skills/s/SKILL.md" {
		t.Errorf("project must beat user, got %q", got)
	}
	got, _ := Resolve(has("/h/.codex/skills/s/SKILL.md"), "codex", "s", "/r", "/h")
	if got != "/h/.codex/skills/s/SKILL.md" {
		t.Errorf("codex second user location: got %q", got)
	}
	got, looked := Resolve(has(), "codex", "s", "/r", "/h")
	if got != "" || len(looked) != 3 {
		t.Errorf("absent: found %q looked %v", got, looked)
	}
	if got, looked := Resolve(has(), "nope", "s", "/r", "/h"); got != "" || looked != nil {
		t.Errorf("unknown kind: found %q looked %v", got, looked)
	}
}
