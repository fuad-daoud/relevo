package candidate

import (
	"reflect"
	"strings"
	"testing"
)

func TestDeriveNamesThisMachine(t *testing.T) {
	entries := []Candidate{
		{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high"},
		{Harness: "agy", Provider: "antigravity", Model: "claude-sonnet-4-6"},
		{Harness: "claude", Provider: "anthropic", Model: "sonnet"},
		{Harness: "claude", Provider: "anthropic", Model: "haiku"},
		{Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash"},
		{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.1-flash#high"},
		{Harness: "codex", Provider: "openai", Model: "gpt-5.6-terra:high"},
	}
	want := []string{
		"gemini-3.8-flash-high",
		"claude-sonnet-4-6",
		"sonnet",
		"haiku",
		"glm-5.3-flash",
		"deepseek-v4.1-flash",
		"gpt-5.6-terra",
	}

	got := DeriveNames(entries)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveNames() = %v, want %v", got, want)
	}
}

func TestDeriveNamesCollisions(t *testing.T) {
	tests := []struct {
		name    string
		entries []Candidate
		want    []string
	}{
		{
			name: "same model on two harnesses",
			entries: []Candidate{
				{Harness: "claude", Provider: "anthropic", Model: "sonnet"},
				{Harness: "agy", Provider: "antigravity", Model: "sonnet"},
			},
			want: []string{"sonnet", "agy-sonnet"},
		},
		{
			name: "same model, different efforts",
			entries: []Candidate{
				{Harness: "codex", Provider: "openai", Model: "gpt-x:high"},
				{Harness: "codex", Provider: "openai", Model: "gpt-x:low"},
			},
			want: []string{"gpt-x", "gpt-x-low"},
		},
		{
			name: "an explicit name reserves its word",
			entries: []Candidate{
				{Harness: "claude", Provider: "anthropic", Model: "sonnet"},
				{Harness: "agy", Provider: "antigravity", Model: "sonnet", Name: "sonnet"},
			},
			want: []string{"claude-sonnet", "sonnet"},
		},
		{
			name: "a model matching its provider",
			entries: []Candidate{
				{Harness: "codex", Provider: "openai", Model: "openai"},
			},
			want: []string{"codex-openai"},
		},
		{
			name: "a 40-character model is truncated to 24",
			entries: []Candidate{
				{Harness: "claude", Provider: "anthropic", Model: strings.Repeat("a", 40)},
			},
			want: []string{strings.Repeat("a", 24)},
		},
		{
			name: "three identical bases under one harness",
			entries: []Candidate{
				{Harness: "h", Provider: "p", Model: "x"},
				{Harness: "h", Provider: "p", Model: "x"},
				{Harness: "h", Provider: "p", Model: "x"},
			},
			want: []string{"x", "h-x", "x-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveNames(tt.entries)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DeriveNames() = %v, want %v", got, tt.want)
			}
			for _, n := range got {
				if !IsName(n) {
					t.Errorf("DeriveNames() produced %q, which is not a name", n)
				}
			}
		})
	}
}

func TestIsName(t *testing.T) {
	valid := []string{
		"a",
		"sonnet",
		"deepseek-v4.1-flash",
		"gpt-5.6-terra",
		"x-2",
		strings.Repeat("a", 24),
	}
	for _, s := range valid {
		if !IsName(s) {
			t.Errorf("IsName(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"a/b",
		"-x",
		".x",
		"A",
		"Sonnet",
		"x_y",
		strings.Repeat("a", 25),
		"claude/anthropic/sonnet",
	}
	for _, s := range invalid {
		if IsName(s) {
			t.Errorf("IsName(%q) = true, want false", s)
		}
	}
}
