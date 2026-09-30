package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// chainPlanArg is a --plan value for the refusals that fire before any plan is
// read: the path need not exist, because nothing opens it.
func chainPlanArg(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "plan.md")
}

// TestChainRequiresExactlyOneFeatureFlag pins the label rule at the CLI edge:
// neither flag and both flags are refused, in one line and with exit 2,
// before any runtime is built. CI has no harness, so a test that got past the
// refusal would fail on the environment rather than on the rule.
func TestChainRequiresExactlyOneFeatureFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"neither", []string{"chain", "--name", "shop", "--plan", chainPlanArg(t)}},
		{"both", []string{"chain", "--name", "shop", "--plan", chainPlanArg(t), "--feature", "auth", "--no-feature"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, codeRefused, "")
			if !strings.Contains(ce.message, "--feature") {
				t.Errorf("message = %q, want it to name --feature", ce.message)
			}
		})
	}
}

// TestChainRejectsAnEmptyPlanAtParseTime pins the plan shape at the CLI edge:
// no --plan, and a --plan with no path, are refused before any runtime is
// built.
func TestChainRejectsAnEmptyPlanAtParseTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"none", []string{"chain", "--name", "shop", "--feature", "auth"}},
		{"empty value", []string{"chain", "--name", "shop", "--feature", "auth", "--plan", "  "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, codeUsage, "")
			if !strings.Contains(ce.message, "--plan") {
				t.Errorf("message = %q, want it to name --plan", ce.message)
			}
		})
	}
}

// TestChainRefusesAnUnknownFlag pins that an unknown flag is a usage refusal,
// before any runtime is built.
func TestChainRefusesAnUnknownFlag(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--name", "shop", "--feature", "auth", "--plan", chainPlanArg(t), "--nope"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "nope") {
		t.Errorf("message = %q, want it to name the unknown flag", ce.message)
	}
}
