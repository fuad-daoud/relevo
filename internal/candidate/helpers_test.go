package candidate

import (
	"os"
	"path/filepath"
	"testing"
)

const threeSetBody = `[
	{
		"harness": "claude",
		"provider": "anthropic",
		"model": "sonnet",
		"roles": ["builder", "reviewer"]
	},
	{
		"harness": "opencode",
		"provider": "openrouter",
		"model": "z-ai/glm-5.3-flash",
		"roles": ["builder"]
	},
	{
		"harness": "agy",
		"provider": "google",
		"model": "gemini-3.8-flash-high",
		"roles": ["builder"]
	}
]`

const twoSetBody = `[
	{"harness":"claude","provider":"anthropic","model":"sonnet"},
	{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high"}
]`

const oneSetBody = `[{"harness":"claude","provider":"anthropic","model":"sonnet"}]`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCandidates(t *testing.T, body string) *Set {
	t.Helper()
	set, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return set
}

func parseSet(t *testing.T, body string) *Set {
	t.Helper()
	set, _, err := Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return set
}

func parseWarnings(t *testing.T, body string) (*Set, []string) {
	t.Helper()
	set, warnings, err := Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return set, warnings
}

func loadWarnings(t *testing.T, body string) (*Set, []string) {
	t.Helper()
	set, warnings, err := LoadWithWarnings(writeTemp(t, body))
	if err != nil {
		t.Fatalf("LoadWithWarnings(%s): %v", body, err)
	}
	return set, warnings
}
