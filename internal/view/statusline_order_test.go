package view

import (
	"slices"
	"testing"
)

// TestStatusLineRowsRanksRunningAheadOfFinished pins the finished-last rule at
// the one projection both statusline surfaces read: a done-chain row sorts
// below every running entry, while the running entries keep their incoming
// order and a fork's halted child stays immediately after its parent.
func TestStatusLineRowsRanksRunningAheadOfFinished(t *testing.T) {
	t.Parallel()

	needs := BindingStatus{Name: "needs", Display: "NEEDS YOU"}
	done := BindingStatus{Name: "done", Display: "DONE",
		Chain: &ChainFacts{Status: "done", Plan: 2, Plans: 4}}
	active := BindingStatus{Name: "active", Display: "ACTIVE"}
	parent := BindingStatus{Name: "cc", Display: "ACTIVE",
		Chain: &ChainFacts{Status: "running", Step: "building", Plan: 1, Plans: 1, Children: []string{"cc.1"}}}
	child := BindingStatus{Name: "cc.1", Display: "NEEDS YOU",
		Chain: &ChainFacts{Status: "halted", Parent: "cc"}}

	rep := Report{Bindings: []BindingStatus{needs, done, active, parent, child}}
	rows := StatusLineRows(rep, baseTime)

	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if want := []string{"needs", "active", "cc", "cc.1", "done"}; !slices.Equal(names, want) {
		t.Fatalf("rows = %q, want %q", names, want)
	}
	at := slices.Index(names, "cc")
	if at < 0 || at+1 >= len(names) || names[at+1] != "cc.1" {
		t.Fatalf("fork child parts from its parent: %q", names)
	}
	if last := rows[len(rows)-1]; last.Display != "DONE" || last.Status != "DONE" {
		t.Errorf("last row = display %q status %q, want DONE/DONE", last.Display, last.Status)
	}
}

// TestStatusLineRowsRunningChainPrecedesFinishedChains pins the case the rule
// exists for: one running entry precedes every finished one, whatever the
// names sort as. The finished rows keep their incoming order rather than
// falling into name order.
func TestStatusLineRowsRunningChainPrecedesFinishedChains(t *testing.T) {
	t.Parallel()

	done := func(name string) BindingStatus {
		return BindingStatus{Name: name, Display: "DONE",
			Chain: &ChainFacts{Status: "done", Plan: 1, Plans: 1}}
	}
	rep := Report{Bindings: []BindingStatus{
		done("c"), done("a"), done("b"),
		{Name: "z", Display: "ACTIVE"},
	}}
	rows := StatusLineRows(rep, baseTime)

	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if want := []string{"z", "c", "a", "b"}; !slices.Equal(names, want) {
		t.Fatalf("rows = %q, want %q", names, want)
	}
}
