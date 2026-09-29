package usage

import (
	"os"
	"testing"
	"time"
)

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/home/fuad/projects/relevo":       "-home-fuad-projects-relevo",
		"/home/fuad/.claude/projects":      "-home-fuad--claude-projects",
		"/home/fuad":                       "-home-fuad",
		"/home/fuad/apps/google-cloud-sdk": "-home-fuad-apps-google-cloud-sdk",
		"/home/fuad/.config/nvim":          "-home-fuad--config-nvim",
	}
	for in, want := range cases {
		if got := ProjectSlug(in); got != want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeStreamUsesResultEvent(t *testing.T) {
	got := claudeStream(mustOpen(t, "testdata/claude-stream.jsonl"), "anthropic")
	if len(got) != 1 {
		t.Fatalf("samples = %d, want 1 (the result event)", len(got))
	}
	s := got[0]
	if !s.HasCost || s.USD < 0.0298 || s.USD > 0.03 {
		t.Errorf("cost = %v/%v, want measured 0.0299", s.USD, s.HasCost)
	}
	if s.Tokens != (Tokens{In: 12, CacheWrite: 100, CacheRead: 100, Out: 25}) {
		t.Errorf("tokens = %+v", s.Tokens)
	}
	if s.Model != "claude-sonnet-5" || s.Provider != "anthropic" {
		t.Errorf("model/provider = %q/%q", s.Model, s.Provider)
	}
}

func TestClaudeStreamKilledFallsBackToAssistantEvents(t *testing.T) {
	got := claudeStream(mustOpen(t, "testdata/claude-stream-killed.jsonl"), "anthropic")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 (msg_1, msg_2 deduped)", len(got))
	}
	var sum Tokens
	for _, s := range got {
		if s.HasCost {
			t.Error("assistant events carry no dollars; HasCost must be false")
		}
		sum = sum.Add(s.Tokens)
	}
	if sum != (Tokens{In: 12, CacheWrite: 100, CacheRead: 100, Out: 25}) {
		t.Errorf("sum = %+v", sum)
	}
}

func TestClaudeStreamEmpty(t *testing.T) {
	f, _ := mustTemp(t, "relevo-exit:0\n")
	if got := claudeStream(f, "anthropic"); len(got) != 0 {
		t.Errorf("empty stream = %d samples, want 0", len(got))
	}
}

func TestClaudeProjectWindowCwdAndDedupe(t *testing.T) {
	start := time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	got := claudeProject(os.DirFS("testdata/claude-project"), "/wt", start, end, "anthropic")
	// msg_p1, msg_p2 (once), msg_s1 from the subagent. Not p3 (outside), not p4
	// (other cwd).
	if len(got) != 3 {
		t.Fatalf("samples = %d, want 3: %+v", len(got), got)
	}
	var sum Tokens
	models := map[string]bool{}
	for _, s := range got {
		if s.HasCost {
			t.Error("pane transcripts carry no dollars")
		}
		sum = sum.Add(s.Tokens)
		models[s.Model] = true
	}
	if sum != (Tokens{In: 9, CacheWrite: 50, CacheRead: 70, Out: 57}) {
		t.Errorf("sum = %+v", sum)
	}
	if !models["claude-opus-5"] || !models["claude-haiku-4-5"] {
		t.Errorf("models = %v, want the subagent's model included", models)
	}
}

func TestClaudeProjectMissingDir(t *testing.T) {
	got := claudeProject(os.DirFS(t.TempDir()), "/wt", time.Time{}, time.Now(), "anthropic")
	if len(got) != 0 {
		t.Errorf("empty dir = %d samples", len(got))
	}
}
