package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestAgentsApplyValidatesBeforeItWrites pins that a refused arriving agents
// item leaves the config, the disk and the render queue untouched and is
// recorded as superseded.
func TestAgentsApplyValidatesBeforeItWrites(t *testing.T) {
	t.Parallel()
	big := quoteBase64(strings.Repeat("a", MaxAgentFileBytes+1))
	cases := []struct{ name, key, item string }{
		{"unsafe item key", "../evil", `{"shape":"writer"}`},
		{"unknown kind", "my-agent", `{"shape":"writer","native":{"nonesuch":{"agent":"n"}}}`},
		{"unsafe native agent", "my-agent", `{"shape":"writer","native":{"claude":{"agent":"../x"}}}`},
		{"files kind not native", "my-agent", `{"shape":"writer","native":{"claude":{"agent":"n"}},"files":{"codex":"eA=="}}`},
		{"file over the cap", "my-agent", `{"shape":"writer","native":{"claude":{"agent":"n"}},"files":{"claude":` + big + `}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertAgentRefused(t, tc.key, tc.item)
		})
	}
}

func assertAgentRefused(t *testing.T, key, item string) {
	t.Helper()
	store, applier := agentsStore(t)
	var renders []string
	store = store.WithAgentRenders(func(agent string) { renders = append(renders, agent) })
	before := store.agentSection(t)
	if err := store.applyKey(Agents, key, json.RawMessage(item)); err == nil {
		t.Fatal("applyKey accepted the item, want a refusal")
	}
	if got := store.agentSection(t); got != before {
		t.Errorf("agents section = %s, want the unchanged %s", got, before)
	}
	if len(renders) != 0 {
		t.Errorf("a refused item queued renders %v", renders)
	}
	if path, err := applier.AgentFilePath("claude", "n"); err == nil {
		if _, ok, _ := applier.ReadAgentFile(path); ok {
			t.Error("a refused item wrote a file")
		}
	}
	if stamp, ok, err := store.Doc().ItemWriteStamp("agents/" + key); err != nil || ok {
		t.Errorf("a refused item was stamped: %q, %v, %v", stamp, ok, err)
	}
	revs, err := store.Log(0)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	for _, r := range revs {
		if strings.HasPrefix(r.Source, supersededSourcePrefix) {
			return
		}
	}
	t.Error("no superseded revision recorded")
}

// TestAgentsApplyValidItemLandsWithFilesAndRender pins that validation does
// not stand in the way of a valid native item.
func TestAgentsApplyValidItemLandsWithFilesAndRender(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	var renders []string
	store = store.WithAgentRenders(func(agent string) { renders = append(renders, agent) })
	item := `{"shape":"writer","native":{"claude":{"agent":"n"}},"files":{"claude":"aGk="}}`
	if err := store.applyKey(Agents, "my-agent", json.RawMessage(item)); err != nil {
		t.Fatalf("applyKey: %v", err)
	}
	path, _ := applier.AgentFilePath("claude", "n")
	if got := readAgentFile(t, path); got != "hi" {
		t.Errorf("file = %q, want hi", got)
	}
	if len(renders) != 1 || renders[0] != "my-agent" {
		t.Errorf("renders = %v, want [my-agent]", renders)
	}
}

// TestAgentsDeleteBypassesValidationAndPoisonStaysVisible pins that deleting
// an item never validates it, and that a poisoned store keeps failing capture
// until the item is deleted.
func TestAgentsDeleteBypassesValidationAndPoisonStaysVisible(t *testing.T) {
	t.Parallel()
	store, _ := agentsStore(t)
	poison := agentItemBody(map[string]string{"poisoned": `{"shape":"writer","native":{"claude":{"agent":"../x"}}}`})
	if err := store.db.Tx(func(tx *db.Tx) error {
		return tx.ConfigPut(string(Agents), poison, store.now())
	}); err != nil {
		t.Fatalf("seed poison: %v", err)
	}
	if _, err := store.CaptureAgentFiles(); err == nil {
		t.Fatal("capture of a poisoned store succeeded, want it to keep erroring")
	}
	if err := store.applyKey(Agents, "poisoned", nil); err != nil {
		t.Fatalf("delete of a poisoned item: %v", err)
	}
	if _, err := store.CaptureAgentFiles(); err != nil {
		t.Fatalf("capture after the delete: %v", err)
	}
}
