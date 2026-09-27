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
