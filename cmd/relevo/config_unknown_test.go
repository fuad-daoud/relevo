package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSetRefusesUnknownFieldsOnAgents(t *testing.T) {
	docsEnv(t)

	// Key-path form: agents.x.outcomes
	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "agents.x.outcomes", `["verdict"]`})
	})
	ce := requireCLIError(t, err, codeConfigInvalid, "")
	for _, want := range []string{
		"agents.x.outcomes: unknown field",
		"known: native, shape, source",
		"outcomes are declared on the actor: relevo config set actors.<actor>.outputs '{...}'",
	} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message = %q, want it to contain %q", ce.message, want)
		}
	}

	// Verify nothing was stored in agents section
	out, _, _ := captureOutput(t, func() error {
		return run([]string{"config", "get", "agents"})
	})
	if strings.Contains(string(out), "x") {
		t.Errorf("agents section should not contain x, got %s", out)
	}

	// Whole-entry form: agents.x with stray role
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "set", "agents.x", `{"shape":"reader","native":{"claude":{"agent":"x"}},"role":"reviewer"}`})
	})
	ce = requireCLIError(t, err, codeConfigInvalid, "")
	for _, want := range []string{
		"agents.x.role: unknown field",
		"known: native, shape, source",
		"outcomes are declared on the actor: relevo config set actors.<actor>.outputs '{...}'",
	} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message = %q, want it to contain %q", ce.message, want)
		}
	}
}

func TestConfigSetUnknownAgentTypoGetsNoOutcomesNote(t *testing.T) {
	docsEnv(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "agents.x.sourc", `"agents/x.md"`})
	})
	ce := requireCLIError(t, err, codeConfigInvalid, "")
	if !strings.Contains(ce.message, "agents.x.sourc: unknown field") {
		t.Errorf("message = %q, want it to name agents.x.sourc", ce.message)
	}
	if strings.Contains(ce.message, "outcomes are declared") {
		t.Errorf("message = %q, a typo should not point at actor outputs", ce.message)
	}
}

func TestConfigSetRefusesUnknownFieldsOnActors(t *testing.T) {
	docsEnv(t)

	// Key-path form: actors.x.role
	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "actors.x.role", `"reviewer"`})
	})
	ce := requireCLIError(t, err, codeConfigInvalid, "")
	if !strings.Contains(ce.message, "actors.x.role: unknown field") {
		t.Errorf("message = %q, want it to name actors.x.role", ce.message)
	}
	if !strings.Contains(ce.message, "known: agent, candidates, check, outputs, placement, scope, tier") {
		t.Errorf("message = %q, want it to list known fields", ce.message)
	}
	if strings.Contains(ce.message, "outcomes are declared") {
		t.Errorf("message = %q, actors section should not mention outcomes declaration note", ce.message)
	}

	// Verify section is empty
	out, _, _ := captureOutput(t, func() error {
		return run([]string{"config", "get", "actors"})
	})
	if strings.Contains(string(out), "x") {
		t.Errorf("actors section should not contain x, got %s", out)
	}
}

func TestConfigSetValidOutputsSucceeds(t *testing.T) {
	docsEnv(t)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "actors.x", `{"agent":"reviewer"}`})
	}); err != nil {
		t.Fatalf("config set actors.x: %v (stderr: %s)", err, stderr)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "actors.x.outputs", `{"verdict":{"one-of":["pass","changes"]}}`})
	}); err != nil {
		t.Fatalf("config set actors.x.outputs: %v (stderr: %s)", err, stderr)
	}

	out, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "actors.x.outputs"})
	})
	if err != nil {
		t.Fatalf("config get: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(out), "verdict") {
		t.Errorf("stored outputs = %s, want verdict", out)
	}
}

func TestConfigSetWithPreExistingStrayKeySucceeds(t *testing.T) {
	docsEnv(t)

	// Pre-seed an agent with a stray key using config import (which does not refuse unknown keys)
	doc := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(doc, []byte(`{
		"agents": {
			"x": {
				"shape": "reader",
				"native": {
					"claude": {
						"agent": "x"
					}
				},
				"outcomes": ["verdict"]
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "import", doc})
	}); err != nil {
		t.Fatalf("config import: %v (stderr: %s)", err, stderr)
	}

	beforeBody, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "agents"})
	})
	if err != nil {
		t.Fatalf("config get agents: %v", err)
	}

	// Refused update must leave stored section byte-identical
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "set", "agents.x.role", `"reviewer"`})
	})
	requireCLIError(t, err, codeConfigInvalid, "")

	afterBody, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "agents"})
	})
	if err != nil {
		t.Fatalf("config get agents: %v", err)
	}
	if !bytes.Equal(beforeBody, afterBody) {
		t.Errorf("refused write modified section: before = %s, after = %s", beforeBody, afterBody)
	}

	// An unrelated config set on another field succeeds and does not fail because of existing outcomes
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "agents.x.shape", `"writer"`})
	}); err != nil {
		t.Fatalf("unrelated config set on existing stray section failed: %v (stderr: %s)", err, stderr)
	}
}
