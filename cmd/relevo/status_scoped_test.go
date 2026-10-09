package main

import (
	"strings"
	"testing"
)

// TestStatusNarrowScopeRowsAreUnchanged pins that the scoped build names what
// the narrow listing named before: a1, a2 and the chain that stands in for its
// members.
func TestStatusNarrowScopeRowsAreUnchanged(t *testing.T) {
	seedTwoMastermindScope(t)
	t.Setenv("RELEVO_MASTERMIND", "architect-b")

	out, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--mastermind", "architect-a", "--json"})
	})
	if err != nil {
		t.Fatalf("status --mastermind architect-a: %v (stderr: %s)", err, stderr)
	}
	if names := reportNames(t, out); !equalNameSets(names, []string{"a1", "a2", "cha"}) {
		t.Errorf("rows = %v, want [a1 a2 cha]", names)
	}

	// The human listing names the same rows as the document.
	out, stderr, err = captureOutput(t, func() error {
		return run([]string{"status", "--mastermind", "architect-a"})
	})
	if err != nil {
		t.Fatalf("status --mastermind architect-a: %v (stderr: %s)", err, stderr)
	}
	if names := textRowNames(t, string(out)); !equalNameSets(names, []string{"a1", "a2", "cha"}) {
		t.Errorf("text rows = %v, want [a1 a2 cha]", names)
	}
}

// TestStatusAllMasterMindsKeepsEveryMastermind pins that the widest scope is
// the one that narrows nothing: both masterminds' rows come back.
func TestStatusAllMasterMindsKeepsEveryMastermind(t *testing.T) {
	seedTwoMastermindScope(t)
	t.Setenv("RELEVO_MASTERMIND", "architect-a")

	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"status", "--all-masterminds", "--json"}, []string{"a1", "a2", "b1", "cha", "chb"}},
		{[]string{"status", "--all-masterminds", "--all", "--json"}, []string{"a1", "a2", "a-done", "b1", "cha", "chb"}},
	} {
		out, stderr, err := captureOutput(t, func() error { return run(c.args) })
		if err != nil {
			t.Fatalf("run(%v): %v (stderr: %s)", c.args, err, stderr)
		}
		if names := reportNames(t, out); !equalNameSets(names, c.want) {
			t.Errorf("run(%v) rows = %v, want %v", c.args, names, c.want)
		}
	}
}

// TestStatusNameBypassesNarrowing pins that a named view takes no scope
// narrowing at all: the DONE row is asked for by name and comes back.
func TestStatusNameBypassesNarrowing(t *testing.T) {
	seedTwoMastermindScope(t)
	t.Setenv("RELEVO_MASTERMIND", "architect-a")

	out, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--name", "a-done", "--json"})
	})
	if err != nil {
		t.Fatalf("status --name a-done: %v (stderr: %s)", err, stderr)
	}
	if names := reportNames(t, out); !equalNameSets(names, []string{"a-done"}) {
		t.Errorf("rows = %v, want the DONE row by name", names)
	}
	// Asking for one binding by name is not a scoped view, so no DONE row was
	// hidden and the document says so by not counting any.
	if strings.Contains(string(out), "done_hidden") {
		t.Errorf("status --name a-done counted hidden DONE rows:\n%s", out)
	}

	// An unknown name is still a refusal, not a narrowed-away row.
	_, _, err = captureOutput(t, func() error {
		return run([]string{"status", "--name", "ghost", "--json"})
	})
	requireCLIError(t, err, codeBindingNotFound, "")
}
