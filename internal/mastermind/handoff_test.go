package mastermind

// Pins the injected guide against the hook output.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHookOutputCarriesTheGuide pins that additionalContext starts with the
// mastermind sentence and ends with the guide.
func TestHookOutputCarriesTheGuide(t *testing.T) {
	rec := Record{ID: "pl_aaaaaaaaaaaa", Name: "architect-1"}

	var env hookEnvelope
	if err := json.Unmarshal(HookOutput(rec), &env); err != nil {
		t.Fatalf("HookOutput is not the hook envelope: %v", err)
	}
	ctx := env.HookSpecificOutput.AdditionalContext
	if !strings.HasPrefix(ctx, hookContext(rec)) {
		t.Errorf("additionalContext %q does not start with %q", ctx, hookContext(rec))
	}
	if !strings.HasSuffix(ctx, Guide()) {
		t.Error("additionalContext does not end with the guide")
	}
}

// TestGuideNamesTheLabelFlags pins that the injected guide shows how a fresh
// bind chooses a feature, and states the exactly-one rule and the ticket flag,
// so a MasterMind reading it does not run a refused bind.
func TestGuideNamesTheLabelFlags(t *testing.T) {
	g := Guide()
	for _, want := range []string{"--feature", "--no-feature", "--ticket", "exactly one"} {
		if !strings.Contains(g, want) {
			t.Errorf("the guide does not name %q:\n%s", want, g)
		}
	}
	if !strings.Contains(g, "relevo bind --resume") {
		t.Error("the guide lost its resume bullet")
	}
}

// TestGuideNamesTheChainsCommand pins that the injected guide teaches chains:
// the section, the start and resume commands, the wait and trace commands, and
// the rule that a running chain's members are not driven by hand.
func TestGuideNamesTheChainsCommand(t *testing.T) {
	g := Guide()
	for _, want := range []string{
		"## Chains",
		"relevo chain --name",
		"--plan",
		"relevo wait --name",
		"relevo show <n> --trace",
		"relevo chain --resume --name",
		"Never drive a running chain's members by hand",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("the guide does not name %q:\n%s", want, g)
		}
	}
}
