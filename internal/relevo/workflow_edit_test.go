package relevo

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// editSource is a workflow that needs no actor, so the loop's validation has
// nothing but the format rules to weigh.
const editSource = `name: custom
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

// editSeedSource is a workflow whose one step names a seed file: the shape add
// embeds when it saves the workflow.
const editSeedSource = `name: custom
start: build
steps:
  build: { run: builder, seed: "file:prompt.txt", on: { done: done } }
`

// editBuildSource is editSeedSource with no seed, so a file: reference on it is
// one the stored workflow never carried.
const editBuildSource = `name: custom
start: build
steps:
  build: { run: builder, on: { done: done } }
`

// editActors is the one actor every edit fixture needs: a writer with no
// declared outcomes, so a run step covers done.
func editActors() map[string]workflow.ActorInfo {
	return map[string]workflow.ActorInfo{"builder": {Shape: workflow.ShapeWriter}}
}

// embedSeed parses source and sets its one step's seed to embedded, the pair
// add stores when it saves a workflow with a file: seed.
func embedSeed(t *testing.T, source, embedded string) workflow.Definition {
	t.Helper()
	def, err := workflow.Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	step := def.Steps["build"]
	step.Seed = embedded
	def.Steps["build"] = step
	return def
}

func TestWorkflowEditRoundPrefixesProblems(t *testing.T) {
	edited := []byte("name: broken\nstart: nowhere\nsteps: {}\n")
	stored, def, problems, done := WorkflowEditRound(nil, edited, config.StoredWorkflow{}, nil)
	if done {
		t.Fatal("done = true for an invalid workflow")
	}
	if stored != nil {
		t.Errorf("stored = %q, want nil", stored)
	}
	if def.Name != "" {
		t.Errorf("def = %+v, want the zero definition", def)
	}
	if len(problems) == 0 {
		t.Fatal("no problems for an invalid workflow")
	}
	for _, p := range problems {
		if !strings.HasPrefix(p, "# ") {
			t.Errorf("problem %q is not prefixed with %q", p, "# ")
		}
	}
}

func TestWorkflowEditRoundUnchangedIsNoop(t *testing.T) {
	old := []byte(editSource)
	saved := config.StoredWorkflow{Source: editSource}

	stored, def, problems, done := WorkflowEditRound(old, old, saved, nil)
	if !done || stored != nil || problems != nil || def.Name != "" {
		t.Fatalf("unchanged = (%q, %+v, %v, %v), want (nil, zero, nil, true)", stored, def, problems, done)
	}

	stored, def, problems, done = WorkflowEditRound(old, []byte("  \n"), saved, nil)
	if !done || stored != nil || problems != nil || def.Name != "" {
		t.Fatalf("empty = (%q, %+v, %v, %v), want (nil, zero, nil, true)", stored, def, problems, done)
	}
}

func TestWorkflowEditRoundKeepsEmbeddedFileSeed(t *testing.T) {
	saved := config.StoredWorkflow{
		Source:     editSeedSource,
		Definition: embedSeed(t, editSeedSource, "hello seed\n"),
	}
	edited := []byte(editSeedSource + "# a comment\n")

	stored, def, problems, done := WorkflowEditRound(nil, edited, saved, editActors())
	if !done {
		t.Fatalf("done = false, problems = %v", problems)
	}
	if problems != nil {
		t.Errorf("problems = %v, want none", problems)
	}
	if string(stored) != string(edited) {
		t.Errorf("stored = %q, want the edited buffer", stored)
	}
	if got := def.Steps["build"].Seed; got != "hello seed\n" {
		t.Errorf("build seed = %q, want the embedded contents", got)
	}
}

func TestWorkflowEditRoundRefusesUnresolvedFileSeed(t *testing.T) {
	cases := map[string]struct {
		saved  config.StoredWorkflow
		edited string
	}{
		"a new file: reference": {
			saved: config.StoredWorkflow{Source: editBuildSource, Definition: embedSeed(t, editBuildSource, "")},
			edited: strings.Replace(editBuildSource,
				"run: builder,", `run: builder, seed: "file:prompt.txt",`, 1),
		},
		"a changed path": {
			saved: config.StoredWorkflow{Source: editSeedSource, Definition: embedSeed(t, editSeedSource, "hello seed\n")},
			edited: strings.Replace(editSeedSource,
				"file:prompt.txt", "file:changed.txt", 1),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stored, def, problems, done := WorkflowEditRound(nil, []byte(tc.edited), tc.saved, editActors())
			if done {
				t.Fatalf("done = true for %q", tc.edited)
			}
			if stored != nil {
				t.Errorf("stored = %q, want nil", stored)
			}
			if def.Name != "" {
				t.Errorf("def = %+v, want the zero definition", def)
			}
			if len(problems) == 0 {
				t.Fatal("no problems for a file: reference the saved workflow never carried")
			}
			for _, p := range problems {
				if !strings.HasPrefix(p, "# ") {
					t.Errorf("problem %q is not prefixed with %q", p, "# ")
				}
				if !strings.Contains(p, "build") {
					t.Errorf("problem %q does not name build", p)
				}
			}
		})
	}
}
