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

// configWorkflowSeedSource is a workflow whose one step names a seed file, the
// shape add embeds when it saves the workflow.
const configWorkflowSeedSource = `name: custom
start: build
steps:
  build: { run: builder, seed: "file:prompt.txt", on: { done: done } }
`

// seededPrompt is the contents of the seed file fixture, embedded into a saved
// workflow's definition when it is added.
const seededPrompt = "hello seed\n"

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

// writeSeededWorkflow writes a seed file and a workflow naming it into one temp
// dir and returns the workflow file's path, so add embeds the seed's contents.
func writeSeededWorkflow(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte(seededPrompt), 0o644); err != nil {
		t.Fatalf("write prompt.txt: %v", err)
	}
	path := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(path, []byte(configWorkflowSeedSource), 0o644); err != nil {
		t.Fatalf("write seeded workflow: %v", err)
	}
	return path
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

// appendEditedLine is the editor script `config workflow edit` tests share: it
// appends one comment line to the buffer, so the edit is valid and changed.
const appendEditedLine = `printf '\n# edited\n' >> "$1"`

func TestConfigWorkflowEditSavesAChange(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeWorkflowFile(t, configWorkflowSource))
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", writeEditor(t, appendEditedLine))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "edit", "custom"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "saved (config version") {
		t.Errorf("edit stdout = %q, want the saved line", stdout)
	}

	src := storedConfig(t).Workflows["custom"].Source
	if !strings.Contains(src, "# edited") {
		t.Errorf("stored source = %q, want the edited line", src)
	}
}

func TestConfigWorkflowEditKeepsEmbeddedFileSeed(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeSeededWorkflow(t))
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", writeEditor(t, appendEditedLine))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "edit", "custom"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "saved (config version") {
		t.Errorf("edit stdout = %q, want the saved line", stdout)
	}

	w := storedConfig(t).Workflows["custom"]
	if !strings.Contains(w.Source, "file:prompt.txt") {
		t.Errorf("stored source = %q, want the file: reference kept", w.Source)
	}
	if got := w.Definition.Steps["build"].Seed; got != seededPrompt {
		t.Errorf("build seed = %q, want the embedded contents", got)
	}
}
