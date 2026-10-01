package relevo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// resolveSource is a workflow that needs no actor, so a resolver test never has
// to build a registry.
const resolveSource = `name: custom
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

// resolveSavedSource is the same workflow under the saved key, so the saved
// entry's definition declares the name it is filed under.
const resolveSavedSource = `name: saved-one
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

func workflowStore(t *testing.T) *config.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return config.Open(d)
}

func saveTestWorkflow(t *testing.T, cs *config.Store, name, source string) {
	t.Helper()
	def, err := workflow.Parse([]byte(source))
	if err != nil {
		t.Fatalf("workflow.Parse: %v", err)
	}
	body, err := config.EncodeWorkflows(map[string]config.StoredWorkflow{
		name: {Source: source, Definition: def},
	})
	if err != nil {
		t.Fatalf("EncodeWorkflows: %v", err)
	}
	if _, err := cs.Put(config.Workflows, body); err != nil {
		t.Fatalf("Put(workflows): %v", err)
	}
}

func TestResolveWorkflowOrder(t *testing.T) {
	cs := workflowStore(t)
	saveTestWorkflow(t, cs, "saved-one", resolveSavedSource)
	rt := Runtime{Config: cs}

	dir := t.TempDir()
	path := filepath.Join(dir, "saved-one")
	if err := os.WriteFile(path, []byte(resolveSource), 0o644); err != nil {
		t.Fatalf("write workflow file: %v", err)
	}

	def, origin, err := ResolveWorkflow(rt, path)
	if err != nil {
		t.Fatalf("ResolveWorkflow(file): %v", err)
	}
	if origin != "file" || def.Name != "custom" {
		t.Errorf("file resolution = (%q, %q), want (custom, file)", def.Name, origin)
	}

	def, origin, err = ResolveWorkflow(rt, "saved-one")
	if err != nil {
		t.Fatalf("ResolveWorkflow(saved): %v", err)
	}
	if origin != "saved" || def.Name != "saved-one" {
		t.Errorf("saved resolution = (%q, %q), want (saved-one, saved)", def.Name, origin)
	}

	def, origin, err = ResolveWorkflow(rt, "default")
	if err != nil {
		t.Fatalf("ResolveWorkflow(shipped): %v", err)
	}
	if origin != "shipped" || def.Name != workflow.Default().Name {
		t.Errorf("shipped resolution = (%q, %q), want (%q, shipped)", def.Name, origin, workflow.Default().Name)
	}

	if _, _, err := ResolveWorkflow(rt, "no-such-workflow"); !errors.Is(err, ErrRefused) {
		t.Errorf("unknown workflow error = %v, want ErrRefused", err)
	}
}

func TestEmbedFileSeedsRelative(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("hello seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	def := workflow.Definition{Steps: map[string]workflow.Step{
		"build": {Run: "builder", Seed: "file:prompt.txt"},
	}}

	got, err := EmbedFileSeeds(def, dir)
	if err != nil {
		t.Fatalf("EmbedFileSeeds: %v", err)
	}
	if want := "hello seed\n"; got.Steps["build"].Seed != want {
		t.Errorf("embedded seed = %q, want %q", got.Steps["build"].Seed, want)
	}
}

func TestEmbedFileSeedsRefusesEscape(t *testing.T) {
	dir := t.TempDir()
	for _, seed := range []string{"file:../outside.txt", "file:/etc/hosts", "file:"} {
		def := workflow.Definition{Steps: map[string]workflow.Step{
			"build": {Run: "builder", Seed: seed},
		}}
		got, err := EmbedFileSeeds(def, dir)
		if err == nil {
			t.Errorf("EmbedFileSeeds(%q) = %+v, want a refusal", seed, got)
			continue
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("EmbedFileSeeds(%q) error = %v, want ErrRefused", seed, err)
		}
	}
}
