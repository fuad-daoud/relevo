package relevo

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// customSource is a valid agent source that renders the given kinds.
func customSource(name, kinds string) string {
	return "---\n" +
		"name: " + name + "\n" +
		"description: Runs one task from a plan.\n" +
		"shape: writer\n" +
		"output: report\n" +
		"requires: []\n" +
		"kinds: [" + kinds + "]\n" +
		"---\n\n" +
		"Do the thing.\n"
}

func TestCustomAgentDocsRendersSourceAgentsOnly(t *testing.T) {
	source := customSource("my-executor", "claude, codex")
	agents := map[string]roles.AgentEntry{
		"my-executor": {Source: source},
		"their-agent": {Shape: "writer", Native: map[string]roles.DefRow{
			"claude": {Agent: "their-native"},
		}},
	}

	docs, err := CustomAgentDocs(agents, "")
	if err != nil {
		t.Fatalf("CustomAgentDocs: %v", err)
	}
	wantKinds := []string{"claude", "codex"}
	if len(docs) != len(wantKinds) {
		t.Fatalf("docs = %+v, want one per rendered kind", docs)
	}

	src, err := agentsrc.Parse([]byte(source))
	if err != nil {
		t.Fatalf("agentsrc.Parse: %v", err)
	}
	for i, doc := range docs {
		if doc.Kind != wantKinds[i] || doc.Name != "my-executor" {
			t.Errorf("doc %d = (%s, %s), want (%s, my-executor)", i, doc.Kind, doc.Name, wantKinds[i])
		}
		want, err := agentsrc.Render(src, doc.Kind)
		if err != nil {
			t.Fatalf("agentsrc.Render(%s): %v", doc.Kind, err)
		}
		if !bytes.Equal(doc.Bytes, want) {
			t.Errorf("doc %d bytes = %q, want %q", i, doc.Bytes, want)
		}
	}
}

func TestCustomAgentDocsOnlyFiltersByName(t *testing.T) {
	agents := map[string]roles.AgentEntry{
		"alpha-agent": {Source: customSource("alpha-agent", "claude")},
		"beta-agent":  {Source: customSource("beta-agent", "claude")},
	}

	docs, err := CustomAgentDocs(agents, "beta-agent")
	if err != nil {
		t.Fatalf("CustomAgentDocs: %v", err)
	}
	if len(docs) != 1 || docs[0].Name != "beta-agent" || docs[0].Kind != "claude" {
		t.Fatalf("docs = %+v, want only beta-agent on claude", docs)
	}
}

func TestInstallCustomAgentsFromAStore(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write a fake claude: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := config.Open(d)

	agents := map[string]roles.AgentEntry{"my-executor": {Source: customSource("my-executor", "claude")}}
	body, err := json.Marshal(agents)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := st.As("test", "install custom agents").Put(config.Agents, body); err != nil {
		t.Fatalf("Put(agents): %v", err)
	}

	env := harness.OSInstallEnvKV(d)
	results, err := InstallCustomAgents(st, env, harness.InstallOptions{})
	if err != nil {
		t.Fatalf("InstallCustomAgents: %v", err)
	}
	if len(results) != 1 || results[0].Kind != "claude" || results[0].Outcome != harness.OutcomeWrote {
		t.Fatalf("results = %+v, want one claude write", results)
	}

	src, err := agentsrc.Parse([]byte(customSource("my-executor", "claude")))
	if err != nil {
		t.Fatalf("agentsrc.Parse: %v", err)
	}
	want, err := agentsrc.Render(src, "claude")
	if err != nil {
		t.Fatalf("agentsrc.Render: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "agents", "my-executor.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(raw, want) {
		t.Errorf("file on disk = %q, want %q", raw, want)
	}
}
