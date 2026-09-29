package harness

import (
	"strings"
	"testing"
)

func TestPlanExecutorRoundTripSectionIsSharedAcrossKinds(t *testing.T) {
	const heading = "EVERY MODEL STEP IS A ROUND TRIP"
	const cost = "A round's cost is its number of steps, not its tokens."

	for _, kind := range []string{"claude", "opencode", "agy", "codex"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		s := string(doc)
		if !strings.Contains(collapseWhitespace(s), heading) {
			t.Errorf("plan-executor.%s must contain %q", kind, heading)
		}
		if !strings.Contains(collapseWhitespace(s), cost) {
			t.Errorf("plan-executor.%s must contain %q", kind, cost)
		}
	}

	const end = "costs more round trips than reading them yourself in one batched step."

	var first, firstKind string
	for _, kind := range []string{"claude", "opencode", "agy"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		s := string(doc)
		start := strings.Index(s, heading)
		if start < 0 {
			t.Fatalf("plan-executor.%s: no %q to compare", kind, heading)
		}
		rest := s[start:]
		stop := strings.Index(rest, end)
		if stop < 0 {
			t.Fatalf("plan-executor.%s: round-trip section never reaches %q", kind, end)
		}
		got := rest[:stop+len(end)]
		if first == "" {
			first, firstKind = got, kind
			continue
		}
		if got != first {
			t.Errorf("plan-executor.%s round-trip section differs from plan-executor.%s:\ngot:\n%s\nwant:\n%s", kind, firstKind, got, first)
		}
	}
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
