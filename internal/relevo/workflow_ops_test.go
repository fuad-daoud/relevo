package relevo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// workflowOpsSource is a workflow that names no actor, so a test about the
// section's bookkeeping never has to build a registry.
const workflowOpsSource = `name: custom
# keep this comment
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

// workflowOpsOtherSource is a second saved workflow under another name, so a
// test can prove a removal or an overwrite leaves the rest alone.
const workflowOpsOtherSource = `name: other
params: { gate: true }
start: gate
steps:
  gate: { when: "{{params.gate}}", on: { true: done, false: done } }
`

// writeOpsWorkflow writes body to a workflow file in a fresh temp dir and
// returns the path.
func writeOpsWorkflow(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

func opsRuntime(t *testing.T) Runtime {
	t.Helper()
	return Runtime{Config: workflowStore(t)}
}

func TestWorkflowAddRefusesExistingWithoutReplace(t *testing.T) {
	rt := opsRuntime(t)
	path := writeOpsWorkflow(t, workflowOpsSource)
	if _, err := WorkflowAdd(rt, path, false, false); err != nil {
		t.Fatalf("first add: %v", err)
	}

	_, err := WorkflowAdd(rt, path, false, false)
	if !errors.Is(err, ErrWorkflowSaved) {
		t.Fatalf("second add without --replace: %v, want ErrWorkflowSaved", err)
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Errorf("error = %q, want it to name --replace", err)
	}

	if _, err := WorkflowAdd(rt, path, true, false); err != nil {
		t.Fatalf("add --replace: %v", err)
	}
}

func TestWorkflowAddRefusesShippedNameWithoutForce(t *testing.T) {
	rt := opsRuntime(t)
	body := strings.Replace(workflowOpsSource, "name: custom", "name: default", 1)
	path := writeOpsWorkflow(t, body)

	_, err := WorkflowAdd(rt, path, false, false)
	if !errors.Is(err, ErrWorkflowShipped) {
		t.Fatalf("add of the shipped name without --force: %v, want ErrWorkflowShipped", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to name --force", err)
	}

	if _, err := WorkflowAdd(rt, path, false, true); err != nil {
		t.Fatalf("add --force: %v", err)
	}
	if _, ok := loadWorkflowsOrFail(t, rt)["default"]; !ok {
		t.Error("the forced add did not shadow the shipped name")
	}
}

func TestWorkflowRemoveRefusesShipped(t *testing.T) {
	rt := opsRuntime(t)

	err := WorkflowRemove(rt, workflow.Default().Name)
	if !errors.Is(err, ErrWorkflowNotSaved) {
		t.Fatalf("removing the shipped workflow: %v, want ErrWorkflowNotSaved", err)
	}
	if err.Error() == "" {
		t.Error("the refusal carries no message")
	}
}

func TestWorkflowRemoveDropsOnlyTheNamedWorkflow(t *testing.T) {
	rt := opsRuntime(t)
	for _, src := range []string{workflowOpsSource, workflowOpsOtherSource} {
		if _, err := WorkflowAdd(rt, writeOpsWorkflow(t, src), false, false); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	if err := WorkflowRemove(rt, "custom"); err != nil {
		t.Fatalf("WorkflowRemove: %v", err)
	}
	saved := loadWorkflowsOrFail(t, rt)
	if _, ok := saved["custom"]; ok {
		t.Error("the named workflow is still saved")
	}
	if _, ok := saved["other"]; !ok {
		t.Error("removing one workflow dropped another")
	}
}

func TestWorkflowListMarksShippedAndSaved(t *testing.T) {
	rt := opsRuntime(t)
	for _, src := range []string{workflowOpsSource, workflowOpsOtherSource} {
		if _, err := WorkflowAdd(rt, writeOpsWorkflow(t, src), false, false); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	list, err := WorkflowList(rt)
	if err != nil {
		t.Fatalf("WorkflowList: %v", err)
	}
	want := []struct{ name, origin string }{
		{workflow.Default().Name, WorkflowOriginShipped},
		{"custom", WorkflowOriginSaved},
		{"other", WorkflowOriginSaved},
	}
	if len(list) != len(want) {
		t.Fatalf("WorkflowList = %d rows, want %d", len(list), len(want))
	}
	for i, w := range want {
		if list[i].Name != w.name || list[i].Origin != w.origin {
			t.Errorf("row %d = (%q, %q), want (%q, %q)", i, list[i].Name, list[i].Origin, w.name, w.origin)
		}
	}
}

func TestWorkflowListCarriesDescriptionInputsAndParams(t *testing.T) {
	list, err := WorkflowList(Runtime{})
	if err != nil {
		t.Fatalf("WorkflowList: %v", err)
	}
	shipped := list[0]
	if shipped.Description == "" {
		t.Error("the shipped workflow carries no description")
	}
	if len(shipped.Params) == 0 {
		t.Fatal("the shipped workflow declares no params")
	}
	// Params are listed by name, so the first is the alphabetically first.
	if shipped.Params[0].Name != "builder" || shipped.Params[0].Value != "builder" {
		t.Errorf("first param = %+v, want builder of kind string", shipped.Params[0])
	}
	if shipped.Inputs.Plans != workflow.InputRequired {
		t.Errorf("shipped plans input = %q, want required", shipped.Inputs.Plans)
	}
}

func TestWorkflowSourceReportsShippedAndSaved(t *testing.T) {
	rt := opsRuntime(t)
	if _, err := WorkflowAdd(rt, writeOpsWorkflow(t, workflowOpsSource), false, false); err != nil {
		t.Fatalf("add: %v", err)
	}

	source, shipped, err := WorkflowSource(rt, "custom")
	if err != nil {
		t.Fatalf("WorkflowSource(saved): %v", err)
	}
	if shipped {
		t.Error("a saved workflow reports shipped")
	}
	if !strings.Contains(source, "# keep this comment") {
		t.Errorf("source = %q, want the user's comments kept", source)
	}

	if _, shipped, err := WorkflowSource(rt, workflow.Default().Name); err != nil || !shipped {
		t.Errorf("WorkflowSource(shipped) = (_, %v, %v), want (_, true, nil)", shipped, err)
	}
	if _, _, err := WorkflowSource(rt, "no-such-workflow"); !errors.Is(err, ErrWorkflowNotSaved) {
		t.Errorf("WorkflowSource(unknown) error = %v, want ErrWorkflowNotSaved", err)
	}
}

// TestWorkflowValidateProblemsMatchesCLIText pins the order and the wording of
// the problems, which is what the CLI joins into one config_invalid message.
func TestWorkflowValidateProblemsMatchesCLIText(t *testing.T) {
	def, err := workflow.Parse([]byte(`name: broken
start: ghost
steps:
  build: { run: nobody, on: { done: done } }
`))
	if err != nil {
		t.Fatalf("workflow.Parse: %v", err)
	}

	problems := WorkflowValidateProblems(Runtime{}, def)
	if len(problems) < 2 {
		t.Fatalf("problems = %v, want at least two", problems)
	}
	want := []string{
		"rule 1: start \"ghost\" is not a step",
		"step build: rule 2: run names unknown actor \"nobody\"",
	}
	for i, w := range want {
		if !strings.HasPrefix(problems[i], w) {
			t.Errorf("problem %d = %q, want it to start %q", i, problems[i], w)
		}
	}
	if got := joinProblems(problems); !strings.Contains(got, "; ") {
		t.Errorf("joined problems = %q, want them separated by a semicolon", got)
	}
}

// TestWorkflowGraphEdges pins the graph a step view renders: the budget then is
// an edge beside the on targets, and a fork's child workflows are named as the
// step's actors.
func TestWorkflowGraphEdges(t *testing.T) {
	def, err := workflow.Parse([]byte(`name: graphed
start: fan
steps:
  fan: { fork: { each: plans, workflow: child }, on: { done: work } }
  work: { run: builder, budget: { max: 2, per: chain, then: repair }, on: { done: done } }
  repair: { run: builder, on: { done: work } }
  each-plan: { for-each: plans, on: { done: done } }
`))
	if err != nil {
		t.Fatalf("workflow.Parse: %v", err)
	}

	rows := WorkflowGraph(def)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4: %+v", len(rows), rows)
	}

	byID := make(map[string]GraphRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	if _, ok := byID["ghost"]; ok {
		t.Error("a row for a step the definition does not declare")
	}

	if got := byID["fan"].Kind; got != "fork" {
		t.Errorf("fan kind = %q, want fork", got)
	}
	if got := byID["fan"].Actors; got != "child" {
		t.Errorf("fan actors = %q, want the fork's workflow", got)
	}
	if got := byID["each-plan"].Kind; got != "for-each" {
		t.Errorf("each-plan kind = %q, want for-each", got)
	}

	want := map[string][]string{
		"fan":    {"work"},
		"work":   {"repair"},
		"repair": {"work"},
	}
	for id, edges := range want {
		got := byID[id].Edges
		if len(got) != len(edges) {
			t.Errorf("%s edges = %v, want %v", id, got, edges)
			continue
		}
		for i := range edges {
			if got[i] != edges[i] {
				t.Errorf("%s edges = %v, want %v", id, got, edges)
				break
			}
		}
	}
}

// TestWorkflowGraphStartsAtTheDefinitionStart pins the order the chain view
// walks: from the start, breadth-first, then anything unreachable sorted.
func TestWorkflowGraphStartsAtTheDefinitionStart(t *testing.T) {
	def, err := workflow.Parse([]byte(`name: ordered
start: b
steps:
  b: { when: "true", on: { true: a } }
  a: { when: "true", on: { true: done } }
  detached: { when: "true", on: { true: done } }
`))
	if err != nil {
		t.Fatalf("workflow.Parse: %v", err)
	}

	var ids []string
	for _, row := range WorkflowGraph(def) {
		ids = append(ids, row.ID)
	}
	want := []string{"b", "a", "detached"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", ids, want)
	}
}

func TestWorkflowEditCarriesOnlyTheWorkflowsSection(t *testing.T) {
	rt := opsRuntime(t)
	def, err := workflow.Parse([]byte(workflowOpsSource))
	if err != nil {
		t.Fatalf("workflow.Parse: %v", err)
	}

	edit, err := WorkflowEdit(rt, "custom", []byte(workflowOpsSource), def, "add workflow custom")
	if err != nil {
		t.Fatalf("WorkflowEdit: %v", err)
	}
	if len(edit.Sections) != 1 {
		t.Errorf("sections = %v, want only the workflows one", edit.Sections)
	}
	if _, ok := edit.Sections[config.Workflows]; !ok {
		t.Errorf("sections = %v, want %q", edit.Sections, config.Workflows)
	}
	if edit.Name != "custom" || edit.Message != "add workflow custom" {
		t.Errorf("edit = (%q, %q), want (custom, add workflow custom)", edit.Name, edit.Message)
	}
	// Building the edit writes nothing: the cockpit applies it, so a caller can
	// show a preview without a revision.
	if len(loadWorkflowsOrFail(t, rt)) != 0 {
		t.Error("WorkflowEdit stored the workflow itself")
	}
}

// loadWorkflowsOrFail is the tests' own read of the section, so an assertion
// about what was written does not go through the code under test.
func loadWorkflowsOrFail(t *testing.T, rt Runtime) map[string]config.StoredWorkflow {
	t.Helper()
	L, err := rt.Config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return L.Workflows
}
