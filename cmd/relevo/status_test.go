package main

// status_test.go pins a newcomer's first commands: with no MasterMind
// registered anywhere, status lists nothing instead of refusing.

import (
	"strings"
	"testing"
)

// TestStatusOnAFreshMachineListsNothing pins a newcomer's first command: with
// no MasterMind registered anywhere, status succeeds with an empty listing
// instead of refusing for want of one.
func TestStatusOnAFreshMachineListsNothing(t *testing.T) {
	initRoot(t)
	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"status"}) })
	if err != nil {
		t.Fatalf("status on a fresh machine: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(string(stdout), "no bindings") {
		t.Fatalf("stdout = %q, want the empty listing", stdout)
	}
}

// TestStatusShapesOnAFreshMachineSaySoInsteadOfRefusing pins the status
// shapes a statusline or a newcomer runs before any MasterMind registers:
// --line names the missing MasterMind rather than failing the prompt, --line
// --json still refuses (a null mastermind reads as a healthy session), and
// --chains lists nothing.
func TestStatusShapesOnAFreshMachineSaySoInsteadOfRefusing(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--line"}, "relevo: no MasterMind yet"},
		{[]string{"status", "--chains"}, ""},
		{[]string{"status", "--chains", "--json"}, "{"},
	} {
		t.Run(strings.Join(c.args[1:], " "), func(t *testing.T) {
			initRoot(t)
			t.Setenv("CLAUDECODE", "")
			t.Setenv("RELEVO_MASTERMIND", "")
			stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
			if err != nil {
				t.Fatalf("%v on a fresh machine: %v\nstderr: %s", c.args, err, stderr)
			}
			if !strings.Contains(string(stdout), c.want) {
				t.Fatalf("stdout = %q, want %q", stdout, c.want)
			}
		})
	}
	t.Run("line json", func(t *testing.T) {
		initRoot(t)
		t.Setenv("CLAUDECODE", "")
		t.Setenv("RELEVO_MASTERMIND", "")
		_, _, err := captureOutput(t, func() error { return run([]string{"status", "--line", "--json"}) })
		requireCLIError(t, err, codeUsage, "relevo mastermind list")
	})
}
