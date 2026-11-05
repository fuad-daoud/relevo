package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
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

// TestDryRunShowsRepoCleanupWithSourceAndPhase pins the dry run's cleanup
// section: a repository cleanup appears with its source and when word, on the
// step it precedes. Reporting the flag source for a repo command is the
// mutation this test catches.
func TestDryRunShowsRepoCleanupWithSourceAndPhase(t *testing.T) {
	rt, fg := chainRuntime(t)
	fg.rootCommitSHA = reposRootCommit
	st := auditStore(t)
	seedOneRepos(t, st, fmt.Sprintf(`{%q:{"name":"shop","cleanup":"make clean","cleanup_after":"both"}}`, reposKeyOf(t)))
	rt.Config = st

	doc, err := ChainDryRun(context.Background(), rt, ChainOptions{
		Name:  "shop",
		Plans: []string{filepath.Join(t.TempDir(), "plan-1.md")},
	})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}
	if len(doc.Problems) != 0 {
		t.Fatalf("problems = %v, want none", doc.Problems)
	}
	got := RenderChainDryRun(doc)
	for _, want := range []string{
		"cleanup-round: make clean (repo setting) [round, before check]",
		"fix-cleanup-round: make clean (repo setting) [round, before fix-check]",
		"cleanup-chain: make clean (repo setting) [chain, before done]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dry run output missing %q:\n%s", want, got)
		}
	}
}

// TestDryRunOmitsCleanupSectionWithoutCleanup pins the amendment: a chain
// with no cleanup prints no cleanup lines, so the existing graph output stays
// as it is.
func TestDryRunOmitsCleanupSectionWithoutCleanup(t *testing.T) {
	rt, _ := chainRuntime(t)

	doc, err := ChainDryRun(context.Background(), rt, ChainOptions{
		Name:     "shop",
		Plans:    []string{filepath.Join(t.TempDir(), "plan-1.md")},
		Workflow: "default",
	})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}
	if len(doc.Cleanup) != 0 {
		t.Errorf("cleanup = %+v, want none", doc.Cleanup)
	}
	if got := RenderChainDryRun(doc); strings.Contains(got, "cleanup") {
		t.Errorf("dry run output names cleanup without one:\n%s", got)
	}
}

// TestDryRunCleanupSourceAndWhen pins the per-step helpers: a --param
// override wins the source, a step naming both cleanup params runs in both
// phases, a literal step keeps the workflow source with the after word, and
// the before step reads the done edge, then the else.
func TestDryRunCleanupSourceAndWhen(t *testing.T) {
	t.Parallel()

	res := CleanupResolution{Command: "make clean", Source: "--cleanup", After: "both"}
	roundStep := workflow.Step{Cleanup: "{{params.cleanup_round}}", On: map[string]workflow.Target{"done": workflow.StepTarget("check")}}
	if got := dryRunCleanupSource(roundStep, "param-clean", res, map[string]string{"cleanup_round": "param-clean"}); got != "--param cleanup_round" {
		t.Errorf("source with a --param override = %q, want --param cleanup_round", got)
	}
	if got := dryRunCleanupSource(roundStep, "make clean", res, nil); got != "--cleanup" {
		t.Errorf("source with the resolved command = %q, want --cleanup", got)
	}
	if got := dryRunCleanupSource(roundStep, "other", res, nil); got != "workflow" {
		t.Errorf("source with an unmatched command = %q, want workflow", got)
	}
	literal := workflow.Step{Cleanup: "make clean", On: map[string]workflow.Target{"done": workflow.StepTarget("check")}}
	if got := dryRunCleanupSource(literal, "make clean", res, nil); got != "workflow" {
		t.Errorf("source of a literal step = %q, want workflow, never the resolution", got)
	}
	if got := dryRunCleanupWhen(literal, "chain"); got != "chain" {
		t.Errorf("when of a literal step = %q, want the after word", got)
	}
	bothStep := workflow.Step{Cleanup: "{{params.cleanup_round}}{{params.cleanup_chain}}"}
	if got := dryRunCleanupWhen(bothStep, "chain"); got != "both" {
		t.Errorf("when naming both params = %q, want both", got)
	}
	if got := dryRunCleanupBefore(roundStep); got != "check" {
		t.Errorf("before = %q, want the done edge's check", got)
	}
	elseStep := workflow.Step{Cleanup: "make clean", On: map[string]workflow.Target{"else": workflow.StepTarget("g")}}
	if got := dryRunCleanupBefore(elseStep); got != "g" {
		t.Errorf("before without done = %q, want the else edge's g", got)
	}
	bare := workflow.Step{Cleanup: "make clean"}
	if got := dryRunCleanupBefore(bare); got != "" {
		t.Errorf("before without edges = %q, want empty", got)
	}
}

// TestDryRunDirectFixResolvesShipped pins the direct-fix dry run: the shipped
// name resolves with origin shipped, the graph carries a fix-direct step, and
// the shipped seeds validate with no problems.
func TestDryRunDirectFixResolvesShipped(t *testing.T) {
	rt, _ := chainRuntime(t)

	doc, err := ChainDryRun(context.Background(), rt, ChainOptions{
		Name:     "shop",
		Plans:    []string{filepath.Join(t.TempDir(), "plan-1.md")},
		Workflow: "direct-fix",
	})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}
	if doc.Workflow != "direct-fix" || doc.Origin != "shipped" {
		t.Errorf("workflow = (%q, %q), want (direct-fix, shipped)", doc.Workflow, doc.Origin)
	}
	if len(doc.Problems) != 0 {
		t.Errorf("problems = %v, want none", doc.Problems)
	}
	found := false
	for _, s := range doc.Steps {
		if s.ID == "fix-direct" {
			found = true
		}
	}
	if !found {
		t.Errorf("steps = %v, want a fix-direct row", doc.Steps)
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

// customGateWorkflow supplies its own gate, so a dry run over it proves no
// tier is invented for a value no tier supplied.
const customGateWorkflow = `name: customgate
inputs: { plans: required }
params: { gate: "custom check" }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "{{params.gate}}", on: { green: done, red: { halt: "red" } } }
`

// TestChainDryRunNamesTheGateTier pins the dry run's gate line, one case per
// tier: --gate, the repository setting, the policy default and --param gate
// each name their tier, while --no-gate, no gate anywhere and a custom
// workflow's own default leave the gate bare. Resolving to the policy
// default under a repo gate, dropping the annotation, or annotating an
// empty gate is the mutation this test catches.
func TestChainDryRunNamesTheGateTier(t *testing.T) {
	t.Parallel()

	withPolicy := func(t *testing.T) Runtime {
		t.Helper()
		rt, _ := chainRuntime(t)
		rt.Policy = policyWithGate("go vet ./...", nil)
		return rt
	}
	withRepos := func(t *testing.T) Runtime {
		t.Helper()
		rt, fg := chainRuntime(t)
		fg.rootCommitSHA = reposRootCommit
		st := auditStore(t)
		seedOneRepos(t, st, fmt.Sprintf(`{%q:{"name":"shop","gate":"make check"}}`, reposKeyOf(t)))
		rt.Config = st
		rt.Policy = policyWithGate("go vet ./...", nil)
		return rt
	}
	custom := writeWorkflowFile(t, customGateWorkflow)

	cases := []struct {
		name  string
		setup func(t *testing.T) Runtime
		opts  ChainOptions
		want  string
	}{
		{"flag", withPolicy, ChainOptions{Gate: "make check"}, "  gate=make check (--gate)"},
		{"repo setting", withRepos, ChainOptions{}, "  gate=make check (repo setting)"},
		{"policy default", withPolicy, ChainOptions{}, "  gate=go vet ./... (policy.gate.default)"},
		{"--param gate", withPolicy, ChainOptions{Params: map[string]string{"gate": "custom check"}}, "  gate=custom check (--param gate)"},
		{"--no-gate", withPolicy, ChainOptions{NoGate: true}, "  gate="},
		{"no gate anywhere", func(t *testing.T) Runtime { t.Helper(); rt, _ := chainRuntime(t); return rt }, ChainOptions{}, "  gate="},
		{"custom workflow default", withPolicy, ChainOptions{Workflow: custom}, "  gate=custom check"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			opts := c.opts
			opts.Name = "shop"
			opts.Plans = []string{filepath.Join(t.TempDir(), "plan-1.md")}
			doc, err := ChainDryRun(context.Background(), c.setup(t), opts)
			if err != nil {
				t.Fatalf("ChainDryRun: %v", err)
			}
			line := ""
			for _, l := range strings.Split(RenderChainDryRun(doc), "\n") {
				if strings.HasPrefix(l, "  gate=") {
					line = l
				}
			}
			if line != c.want {
				t.Errorf("gate line = %q, want %q", line, c.want)
			}
		})
	}
}

// TestDryRunGateSourceIgnoresAGatelessWorkflow pins the guard: a definition
// with no gate param names no tier, even when the resolution matches the
// unrendered template text. Dropping the HasParam guard is the mutation this
// test catches.
func TestDryRunGateSourceIgnoresAGatelessWorkflow(t *testing.T) {
	t.Parallel()

	def := workflow.Definition{
		Params: map[string]workflow.Param{
			"builder": {Kind: workflow.ParamString, Str: "builder"},
		},
		Steps: map[string]workflow.Step{
			"build": {Run: "builder"},
		},
	}
	res := resolve.GateResolution{Command: "{{params.gate}}", Source: resolve.GateSourceRepo}
	if got := dryRunGateSource(def, ChainOptions{}, res); got != "" {
		t.Errorf("dryRunGateSource without a gate param = %q, want no tier", got)
	}
}

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
