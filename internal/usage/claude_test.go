package usage

import (
	"testing"
)

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
