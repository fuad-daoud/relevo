package relevo

import (
	"strings"
	"testing"
)

// editSource is a workflow that needs no actor, so the loop's validation has
// nothing but the format rules to weigh.
const editSource = `name: custom
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

func TestWorkflowEditRoundPrefixesProblems(t *testing.T) {
	edited := []byte("name: broken\nstart: nowhere\nsteps: {}\n")
	stored, problems, done := WorkflowEditRound(nil, edited, nil)
	if done {
		t.Fatal("done = true for an invalid workflow")
	}
	if stored != nil {
		t.Errorf("stored = %q, want nil", stored)
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

	stored, problems, done := WorkflowEditRound(old, old, nil)
	if !done || stored != nil || problems != nil {
		t.Fatalf("unchanged = (%q, %v, %v), want (nil, nil, true)", stored, problems, done)
	}

	stored, problems, done = WorkflowEditRound(old, []byte("  \n"), nil)
	if !done || stored != nil || problems != nil {
		t.Fatalf("empty = (%q, %v, %v), want (nil, nil, true)", stored, problems, done)
	}
}
