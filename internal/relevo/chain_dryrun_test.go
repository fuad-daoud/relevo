package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestDryRunPrintsGraphActorsPlacementRefs pins what a dry run prints: the
// workflow and its origin, each run step's actor and where the actor's
// placement puts it, the references a step carries, its edges, and the members
// it would create.
func TestDryRunPrintsGraphActorsPlacementRefs(t *testing.T) {
	rt, _ := chainRuntime(t)
	rows := chainRows()
	reviewer := rows["reviewer"]
	reviewer.Placement = []string{"zen", "local"}
	rows["reviewer"] = reviewer
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)

	doc, err := ChainDryRun(context.Background(), rt, ChainOptions{
		Name:     "shop",
		Plans:    []string{filepath.Join(t.TempDir(), "plan-1.md")},
		Workflow: "default",
	})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}
	if len(doc.Problems) != 0 {
		t.Fatalf("problems = %v, want none", doc.Problems)
	}
	got := RenderChainDryRun(doc)
	for _, want := range []string{
		"workflow: default (shipped)",
		"actor=builder",
		"placement=local",
		"placement=zen,local",
		"refs={{params.builder}} {{plans.current}}",
		"done=check",
		"shop-rev",
		"shop-plan",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dry run output missing %q:\n%s", want, got)
		}
	}
}

// TestDryRunStartsNothing pins that a dry run creates nothing: no chain row, no
// member binding and no worktree.
func TestDryRunStartsNothing(t *testing.T) {
	rt, fg := chainRuntime(t)
	_, err := ChainDryRun(context.Background(), rt, ChainOptions{
		Name:     "shop",
		Plans:    []string{filepath.Join(t.TempDir(), "plan-1.md")},
		Workflow: "default",
	})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}
	assertNothingCreated(t, rt, fg, "shop")
	for _, name := range []string{"shop-rev", "shop-plan"} {
		if _, err := rt.Store.Load(name); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding %q exists after a dry run: %v", name, err)
		}
	}
}

// brokenWorkflow has three failures at once: a param name the format refuses,
// a run naming an actor the registry does not define, and a step no path from
// start reaches.
const brokenWorkflow = `name: broken
params: { Bad_Name: true }
start: build
steps:
  build: { run: nobody, on: { done: done } }
  orphan: { run: reviewer, on: { done: done } }
`

// TestDryRunReportsEveryProblem pins that a dry run surfaces every validation
// failure, not only the first, and still returns the document.
func TestDryRunReportsEveryProblem(t *testing.T) {
	rt, _ := chainRuntime(t)
	path := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(path, []byte(brokenWorkflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	doc, err := ChainDryRun(context.Background(), rt, ChainOptions{Name: "shop", Workflow: path})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}
	if len(doc.Problems) < 3 {
		t.Fatalf("problems = %v, want at least three", doc.Problems)
	}
	joined := strings.Join(doc.Problems, "\n")
	for _, want := range []string{"Bad_Name", "nobody", "orphan"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}
}
