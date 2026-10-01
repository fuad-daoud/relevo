package workflow

import "testing"

func TestReachableFollowsOnAndBudgetThen(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  a: { run: builder, on: { done: b } }
  b: { run: builder, budget: { max: 1, per: chain, then: c }, on: { done: done } }
  c: { run: builder, on: { done: done } }
  d: { run: builder, on: { done: done } }
`)
	reach := reachable(def, "a")
	for _, id := range []string{"a", "b", "c"} {
		if !reach[id] {
			t.Fatalf("reachable = %v, want %q", sortedKeys(reach), id)
		}
	}
	if reach["d"] {
		t.Fatalf("reachable = %v, want d excluded", sortedKeys(reach))
	}
}

func TestReachableIsNilWithoutAStart(t *testing.T) {
	def := mustParse(t, `name: sample
start: ghost
steps:
  a: { run: builder, on: { done: done } }`)
	if got := reachable(def, "ghost"); got != nil {
		t.Fatalf("reachable = %v, want nil", got)
	}
}

func TestGraphCycleFindsACycle(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  a: { run: builder, on: { done: b } }
  b: { run: builder, on: { done: a } }
`)
	cycle := graphCycle(stepEdges(def), map[string]bool{"a": true, "b": true})
	if len(cycle) != 2 || cycle[0] != "a" || cycle[1] != "b" {
		t.Fatalf("cycle = %v, want [a b]", cycle)
	}
}

func TestGraphCycleIgnoresAnAcyclicGraph(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  a: { run: builder, on: { done: b } }
  b: { run: builder, on: { done: done } }
`)
	if cycle := graphCycle(stepEdges(def), map[string]bool{"a": true, "b": true}); cycle != nil {
		t.Fatalf("cycle = %v, want nil", cycle)
	}
}

func TestBoundedCyclesAcceptsAListLoop(t *testing.T) {
	def := mustParse(t, `
name: sample
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, on: { done: plans } }
`)
	if cycles := boundedCycles(def); len(cycles) != 0 {
		t.Fatalf("boundedCycles = %v, want none", cycles)
	}
}

func TestBoundedCyclesRejectsAControlOnlyCycle(t *testing.T) {
	def := mustParse(t, `
name: sample
inputs: { plans: required }
params: { flag: true }
start: a
steps:
  a: { when: "{{params.flag}}", on: { true: b, false: done } }
  b: { for-each: plans, on: { next: a, empty: done } }
`)
	cycles := boundedCycles(def)
	if len(cycles) == 0 || cycles[0].Step != "a" {
		t.Fatalf("boundedCycles = %v, want a control-only cycle at a", cycles)
	}
}

func TestBoundedCyclesRejectsACycleWithNoBudget(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  a: { run: builder, on: { done: b } }
  b: { run: builder, on: { done: a } }
`)
	cycles := boundedCycles(def)
	if len(cycles) == 0 || cycles[0].Step != "a" {
		t.Fatalf("boundedCycles = %v, want an unbounded cycle at a", cycles)
	}
}

func TestBoundedCyclesRejectsABudgetThatResetsInsideItsCycle(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  a: { run: builder, budget: { max: 1, per: b, then: done }, on: { done: b } }
  b: { run: builder, on: { done: a } }
`)
	cycles := boundedCycles(def)
	if len(cycles) == 0 || cycles[0].Step != "a" {
		t.Fatalf("boundedCycles = %v, want a resetting budget at a", cycles)
	}
}

func TestDominatorsOfTheDefault(t *testing.T) {
	dom := dominators(Default(), "plans")
	if dom == nil {
		t.Fatal("dominators = nil, want the default's sets")
	}
	if !dominates(dom, "correct", "build-fix") {
		t.Fatalf("correct does not dominate build-fix: %v", dom["build-fix"])
	}
	if !dominates(dom, "plans", "build") {
		t.Fatalf("plans does not dominate build: %v", dom["build"])
	}
	if dominates(dom, "repair", "check") {
		t.Fatal("repair dominates check, but check is reached on a path with no repair")
	}
	if dominates(dom, "review", "build") {
		t.Fatal("review dominates build, but build runs before the first review")
	}
}
