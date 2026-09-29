package harness

import (
	"testing"
)

const agentFilesAgent = "reviewer"

func TestAgentFilesStates(t *testing.T) {
	agyPath := "/home/u/.gemini/config/agents/reviewer.md"
	shipped, err := AgentDoc(agentFilesAgent, "agy")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}
	old := []byte("an older relevo definition\n")
	edited := []byte("---\nname: reviewer\nmodel: haiku\n---\n\nbody\n")

	tests := []struct {
		name      string
		file      []byte // nil = absent
		manifest  map[string]string
		wantState FileState
		wantModel string
	}{
		{"identical file gives up to date", shipped, nil, FileUpToDate, "inherit"},
		{"manifest-matching old copy gives stale", old, map[string]string{".gemini/config/agents/reviewer.md": docSHA(old)}, FileStale, ""},
		{"hand-edited file gives your edit, with its model pin read", edited, nil, FileEdited, "haiku"},
		{"your edit over an older shipped copy gives edit + newer", edited, map[string]string{".gemini/config/agents/reviewer.md": docSHA(old)}, FileEditedNewer, "haiku"},
		{"your edit over the current shipped copy stays your edit", edited, map[string]string{".gemini/config/agents/reviewer.md": docSHA(shipped)}, FileEdited, "haiku"},
		{"absent file gives missing", nil, nil, FileMissing, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := freshEnv()
			env.lookPaths["agy"] = "/bin/agy"
			if tc.file != nil {
				env.files[agyPath] = tc.file
			}
			if tc.manifest != nil {
				env.manifest = tc.manifest
			}

			got, err := AgentFiles(env, agentFilesAgent)
			if err != nil {
				t.Fatalf("AgentFiles: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d entries, want 1: %+v", len(got), got)
			}
			if got[0].Kind != "agy" || got[0].Path != agyPath || got[0].State != tc.wantState {
				t.Errorf("entry = %+v, want agy %s %s", got[0], agyPath, tc.wantState)
			}
			if got[0].Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", got[0].Model, tc.wantModel)
			}
		})
	}
}

func TestAgentFilesSkipsUnavailableKindsAndCustomNames(t *testing.T) {
	t.Run("a kind whose binary is not on PATH is absent from the result", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["claude"] = "/bin/claude"
		shipped, err := AgentDoc(agentFilesAgent, "claude")
		if err != nil {
			t.Fatalf("AgentDoc: %v", err)
		}
		env.files["/home/u/.claude/agents/reviewer.md"] = shipped

		got, err := AgentFiles(env, agentFilesAgent)
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if len(got) != 1 || got[0].Kind != "claude" {
			t.Fatalf("entries = %+v, want only claude", got)
		}
	})

	t.Run("a custom agent name gives nil", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["agy"] = "/bin/agy"

		got, err := AgentFiles(env, "my-own-agent")
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if got != nil {
			t.Fatalf("entries = %+v, want nil", got)
		}
	})
}

func TestResetAgentFileOverwritesAndSettles(t *testing.T) {
	env := freshEnv()
	env.lookPaths["agy"] = "/bin/agy"
	agyPath := "/home/u/.gemini/config/agents/reviewer.md"
	env.files[agyPath] = []byte("my own edit\n")

	res, err := ResetAgentFile(env, "agy", agentFilesAgent)
	if err != nil {
		t.Fatalf("ResetAgentFile: %v", err)
	}
	if res.Outcome != OutcomeOverwrote {
		t.Fatalf("Outcome = %v, want %v", res.Outcome, OutcomeOverwrote)
	}

	shipped, err := AgentDoc(agentFilesAgent, "agy")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}
	if !DocEqual(shipped, env.files[agyPath]) {
		t.Errorf("file on disk is not the shipped copy after reset")
	}

	got, err := AgentFiles(env, agentFilesAgent)
	if err != nil {
		t.Fatalf("AgentFiles: %v", err)
	}
	if len(got) != 1 || got[0].State != FileUpToDate {
		t.Fatalf("entries = %+v, want one %s", got, FileUpToDate)
	}
}
