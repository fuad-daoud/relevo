package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

// TestConfigWorkflowShowAndEditShadowedShippedName pins the shadowing case at
// the CLI: a workflow saved over the shipped name with --force is a saved
// workflow, so show prints its source and edit opens it rather than both
// refusing with the shipped workflow's "is not saved". The mutation is to put
// WorkflowSource's shipped-name short-circuit back in front of its saved
// lookup, which turns both of these into the refusal.
func TestConfigWorkflowShowAndEditShadowedShippedName(t *testing.T) {
	initRoot(t)
	body := strings.Replace(configWorkflowSource, "name: custom", "name: default", 1)
	path := writeWorkflowFile(t, body)
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "add", path, "--force"})
	}); err != nil {
		t.Fatalf("config workflow add --force: %v (stderr: %s)", err, stderr)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "show", "default"})
	})
	if err != nil {
		t.Fatalf("show of the shadowing workflow: %v (stderr: %s)", err, stderr)
	}
	for _, want := range []string{"# keep this comment", "name: default"} {
		if !strings.Contains(string(stdout), want) {
			t.Errorf("show output = %q, want it to contain %q", stdout, want)
		}
	}

	// An editor that leaves the buffer alone ends the loop with "no changes",
	// so this reaches the editor and stays out of a real edit.
	t.Setenv("EDITOR", writeEditor(t, "exit 0"))
	stdout, stderr, err = captureOutput(t, func() error {
		return run([]string{"config", "workflow", "edit", "default"})
	})
	if err != nil {
		t.Fatalf("edit of the shadowing workflow: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "no changes") {
		t.Errorf("edit output = %q, want the editor loop's %q", stdout, "no changes")
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

// TestConfigWorkflowAddJSON pins `config workflow add --json`: stdout is one
// document with empty stderr, carrying the stored name.
func TestConfigWorkflowAddJSON(t *testing.T) {
	initRoot(t)
	path := writeWorkflowFile(t, configWorkflowSource)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "add", path, "--json"})
	})
	if err != nil {
		t.Fatalf("add --json: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("stdout = %q, want exactly one JSON object", stdout)
	}
	var doc ConfigWorkflowAddDoc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if doc.Name != "custom" {
		t.Errorf("doc = %+v, want the stored name custom", doc)
	}
	if _, ok := storedConfig(t).Workflows["custom"]; !ok {
		t.Error("stored workflows lack custom, want the add to have saved it")
	}
}

// TestConfigWorkflowAddHumanUnchanged pins the human default beside the
// document: the stored line, not JSON.
func TestConfigWorkflowAddHumanUnchanged(t *testing.T) {
	initRoot(t)
	path := writeWorkflowFile(t, configWorkflowSource)

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "add", path})
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(string(stdout), "stored workflow custom") {
		t.Errorf("stdout = %q, want the stored line", stdout)
	}
	if json.Valid(stdout) {
		t.Errorf("stdout = %q, want the human line, not a document", stdout)
	}
}

// TestConfigWorkflowRmJSON pins `config workflow rm --json`: one document
// with empty stderr, carrying the removed name.
func TestConfigWorkflowRmJSON(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeWorkflowFile(t, configWorkflowSource))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "rm", "custom", "--json"})
	})
	if err != nil {
		t.Fatalf("rm --json: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("stdout = %q, want exactly one JSON object", stdout)
	}
	var doc ConfigWorkflowRmDoc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if doc.Name != "custom" || !doc.Removed {
		t.Errorf("doc = %+v, want custom removed", doc)
	}
	if _, ok := storedConfig(t).Workflows["custom"]; ok {
		t.Error("stored workflows still hold custom, want it removed")
	}
}

// TestConfigWorkflowRmHumanUnchanged pins the human default beside the
// document: the removed line, not JSON.
func TestConfigWorkflowRmHumanUnchanged(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeWorkflowFile(t, configWorkflowSource))

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "rm", "custom"})
	})
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(string(stdout), "removed workflow custom") {
		t.Errorf("stdout = %q, want the removed line", stdout)
	}
	if json.Valid(stdout) {
		t.Errorf("stdout = %q, want the human line, not a document", stdout)
	}
}

// TestConfigWorkflowEditJSON pins `config workflow edit --json` on the saved
// path: stdout is one document with empty stderr, carrying the workflow name
// and the config version. The stand-in editor is an sh script, never a real
// editor.
func TestConfigWorkflowEditJSON(t *testing.T) {
	initRoot(t)
	addConfigWorkflow(t, writeWorkflowFile(t, configWorkflowSource))
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", writeEditor(t, appendEditedLine))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "workflow", "edit", "custom", "--json"})
	})
	if err != nil {
		t.Fatalf("edit --json: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("stdout = %q, want exactly one JSON object", stdout)
	}
	var doc ConfigWorkflowEditDoc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if doc.Name != "custom" {
		t.Errorf("doc = %+v, want the edited name custom", doc)
	}
	if doc.Version == 0 {
		t.Error("version is zero, want the config version the edit left")
	}
	if src := storedConfig(t).Workflows["custom"].Source; !strings.Contains(src, "# edited") {
		t.Errorf("stored source = %q, want the edited line", src)
	}
}

// assertWorkflowJSONFailure runs one failing workflow verb under --json and
// pins the failure contract: nothing on either stream before report, the
// catalog code and its next hint, and the guide's error document.
func assertWorkflowJSONFailure(t *testing.T, args []string, code errorCode, next string) {
	t.Helper()
	stdout, stderr, runErr := captureOutput(t, func() error { return run(args) })
	ce := requireCLIError(t, runErr, code, next)
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != catalogExit(code) {
		t.Errorf("exit = %v, want %d (code %s)", runErr, catalogExit(code), code)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty before report", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty before report", stderr)
	}
	if ce.message == "" {
		t.Error("message is empty")
	}
	var buf bytes.Buffer
	if got := report(&buf, runErr, true); got != catalogExit(code) {
		t.Errorf("report exit = %d, want %d", got, catalogExit(code))
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Next    string `json:"next"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("envelope %q: %v", buf.String(), err)
	}
	if env.Error.Code != string(code) {
		t.Errorf("code = %q, want %q", env.Error.Code, code)
	}
	if env.Error.Message == "" {
		t.Error("envelope message is empty")
	}
	if next != "" && env.Error.Next != next {
		t.Errorf("next = %q, want %q", env.Error.Next, next)
	}
}

// TestConfigWorkflowAddJSONConflict pins the saved-name refusal under --json:
// a second add without --replace is a conflict with nothing before report.
func TestConfigWorkflowAddJSONConflict(t *testing.T) {
	initRoot(t)
	path := writeWorkflowFile(t, configWorkflowSource)
	addConfigWorkflow(t, path)
	assertWorkflowJSONFailure(t,
		[]string{"config", "workflow", "add", path, "--json"}, codeConflict, "")
}

// TestConfigWorkflowRmJSONNotSaved pins the unknown-name refusal under --json.
func TestConfigWorkflowRmJSONNotSaved(t *testing.T) {
	initRoot(t)
	assertWorkflowJSONFailure(t,
		[]string{"config", "workflow", "rm", "nosuch", "--json"},
		codeConfigPathNotSet, "relevo config export")
}

// TestConfigWorkflowEditJSONNotSaved pins the unknown-name refusal under
// --json on the edit path.
func TestConfigWorkflowEditJSONNotSaved(t *testing.T) {
	initRoot(t)
	assertWorkflowJSONFailure(t,
		[]string{"config", "workflow", "edit", "nosuch", "--json"},
		codeConfigPathNotSet, "relevo config export")
}
