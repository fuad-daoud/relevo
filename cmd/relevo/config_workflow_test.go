package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const configWorkflowSource = `name: custom
# keep this comment
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

func writeWorkflowFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

func addConfigWorkflow(t *testing.T, path string) {
	t.Helper()
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "add", path})
	}); err != nil {
		t.Fatalf("config workflow add: %v (stderr: %s)", err, stderr)
	}
}

func TestConfigWorkflowAddRefusesExistingWithoutReplace(t *testing.T) {
	initRoot(t)
	path := writeWorkflowFile(t, configWorkflowSource)
	addConfigWorkflow(t, path)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "add", path})
	})
	if err == nil {
		t.Fatal("a second add without --replace: want a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Errorf("error = %q, want it to name --replace", err)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "add", path, "--replace"})
	}); err != nil {
		t.Fatalf("add --replace: %v (stderr: %s)", err, stderr)
	}
}

func TestConfigWorkflowAddRefusesShippedNameWithoutForce(t *testing.T) {
	initRoot(t)
	body := strings.Replace(configWorkflowSource, "name: custom", "name: default", 1)
	path := writeWorkflowFile(t, body)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "add", path})
	})
	if err == nil {
		t.Fatal("add of the shipped name without --force: want a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to name --force", err)
	}
}

func TestConfigWorkflowShowKeepsComments(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeWorkflowFile(t, configWorkflowSource))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "show", "custom"})
	})
	if err != nil {
		t.Fatalf("show: %v (stderr: %s)", err, stderr)
	}
	out := string(stdout)
	for _, want := range []string{"# keep this comment", "name: custom"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output = %q, want it to contain %q", out, want)
		}
	}
}

func TestConfigWorkflowShowJSON(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeWorkflowFile(t, configWorkflowSource))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "show", "custom", "--json"})
	})
	if err != nil {
		t.Fatalf("show --json: %v (stderr: %s)", err, stderr)
	}
	var def struct {
		Name  string                     `json:"name"`
		Start string                     `json:"start"`
		Steps map[string]json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(stdout, &def); err != nil {
		t.Fatalf("show --json is not a workflow document: %v\n%s", err, stdout)
	}
	if def.Name != "custom" || def.Start != "gate" {
		t.Errorf("show --json = %+v, want name custom and start gate", def)
	}
	if _, ok := def.Steps["gate"]; !ok {
		t.Errorf("show --json steps = %v, want a gate step", def.Steps)
	}
}

func TestConfigShowListsWorkflows(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeWorkflowFile(t, configWorkflowSource))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config"})
	})
	if err != nil {
		t.Fatalf("config: %v (stderr: %s)", err, stderr)
	}
	out := string(stdout)
	for _, want := range []string{"workflows", "default  shipped", "custom  saved"} {
		if !strings.Contains(out, want) {
			t.Errorf("config output does not contain %q:\n%s", want, out)
		}
	}
}
