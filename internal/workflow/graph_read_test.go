package workflow

import (
	"slices"
	"testing"
)

// graphSample is a workflow whose steps cover every kind the graph readers
// report: a run, a check, a fork with a fixed child list, a for-each, a budget
// with a then, and a step that names no actor at all.
const graphSample = `name: sample
start: fan
steps:
  fan: { fork: { each: plans, workflow: child }, on: { done: build } }
  fixed: { fork: { children: [{ workflow: one }, { workflow: two }] }, on: { done: build } }
  build: { run: builder, budget: { max: 2, per: chain, then: repair }, on: { done: check } }
  check: { check: "{{params.gate}}", on: { green: done, red: repair } }
  repair: { run: builder, on: { done: done } }
  each-plan: { for-each: plans, on: { next: build, empty: done } }
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

func TestStepKindNamesASingleKind(t *testing.T) {
	def := mustParse(t, graphSample)
	for id, want := range map[string]string{
		"fan":       "fork",
		"fixed":     "fork",
		"build":     "run",
		"check":     "check",
		"each-plan": "for-each",
		"gate":      "when",
	} {
		if got := StepKind(def, id); got != want {
			t.Errorf("StepKind(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestStepKindIsEmptyWithoutOneKind(t *testing.T) {
	// A step with two kinds is reported by rule 2, not read as either, so it
	// has no single kind to report.
	def := mustParse(t, `name: sample
start: twin
steps:
  twin: { run: builder, when: "true", on: { done: done } }
`)
	if got := StepKind(def, "twin"); got != "" {
		t.Errorf("StepKind(twin) = %q, want empty for a step with two kinds", got)
	}
	if got := StepKind(def, "ghost"); got != "" {
		t.Errorf("StepKind(ghost) = %q, want empty for a step that does not exist", got)
	}
}

func TestStepEdgesAreTheOnTargetsAndTheBudgetThen(t *testing.T) {
	def := mustParse(t, graphSample)
	edges := StepEdges(def)

	for id, want := range map[string][]string{
		"fan":   {"build"},
		"fixed": {"build"},
		// build carries both an on target and a budget then, so it has two edges.
		"build":     {"check", "repair"},
		"check":     {"repair"},
		"each-plan": {"build"},
	} {
		if !slices.Equal(edges[id], want) {
			t.Errorf("StepEdges(%q) = %v, want %v", id, edges[id], want)
		}
	}
	// done and halt are sinks: neither names a step, so neither is an edge.
	for _, id := range []string{"repair", "gate"} {
		if len(edges[id]) != 0 {
			t.Errorf("StepEdges(%q) = %v, want no edge to a sink", id, edges[id])
		}
	}
}

func TestStepActorsNamesTheActorsAStepDrives(t *testing.T) {
	def := mustParse(t, graphSample)
	for id, want := range map[string]string{
		"fan":       "child",
		"fixed":     "one, two",
		"build":     "builder",
		"check":     "{{params.gate}}",
		"each-plan": "",
		"gate":      "",
	} {
		if got := StepActors(def, id); got != want {
			t.Errorf("StepActors(%q) = %q, want %q", id, got, want)
		}
	}
	if got := StepActors(def, "ghost"); got != "" {
		t.Errorf("StepActors(ghost) = %q, want empty", got)
	}
}

// TestStepActorsOfADefaultStep reads the shipped default, whose actors are
// param references: the graph shows what the definition names, not what a chain
// resolves the name to.
func TestStepActorsOfADefaultStep(t *testing.T) {
	if got := StepActors(Default(), "build"); got != "{{params.builder}}" {
		t.Errorf("StepActors(default build) = %q, want the param reference it names", got)
	}
}
