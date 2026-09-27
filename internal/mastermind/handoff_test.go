package mastermind

// Pins handoff.md against every shipped architect copy's "Handing off"
// section, without touching those copies.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// handingOffSection returns doc from "## Handing off" to the end, or to the
// first line exactly delim when delim is non-empty.
func handingOffSection(t *testing.T, doc []byte, delim string) []byte {
	t.Helper()
	i := bytes.Index(doc, []byte("## Handing off"))
	if i < 0 {
		t.Fatal("the definition has no '## Handing off' heading")
	}
	rest := doc[i:]
	if delim == "" {
		return rest
	}
	var out []byte
	for _, line := range bytes.Split(rest, []byte("\n")) {
		if string(line) == delim {
			break
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}

// TestHandoffRulesMatchShippedArchitectCopies pins handoff.md byte-identical
// to the "Handing off" section of every shipped architect copy.
func TestHandoffRulesMatchShippedArchitectCopies(t *testing.T) {
	for _, kind := range []string{"claude", "opencode", "agy"} {
		doc, err := harness.AgentDoc("architect", kind)
		if err != nil {
			t.Fatalf("AgentDoc(architect, %s): %v", kind, err)
		}
		got := handingOffSection(t, doc, "")
		if !bytes.Equal(got, []byte(handoffRules)) {
			t.Errorf("%s: architect's Handing off section differs from handoffRules", kind)
		}
	}

	// codex wraps its definition in a TOML literal string ending in '''.
	doc, err := harness.AgentDoc("architect", "codex")
	if err != nil {
		t.Fatalf("AgentDoc(architect, codex): %v", err)
	}
	got := handingOffSection(t, doc, "'''")
	if !bytes.Equal(got, []byte(handoffRules)) {
		t.Errorf("codex: architect's Handing off section up to the delimiter differs from handoffRules")
	}
}

// TestHookOutputCarriesHandoffRules pins that additionalContext starts with
// the mastermind sentence and ends with the handoff rules.
func TestHookOutputCarriesHandoffRules(t *testing.T) {
	rec := Record{ID: "pl_aaaaaaaaaaaa", Name: "architect-1"}

	var env hookEnvelope
	if err := json.Unmarshal(HookOutput(rec), &env); err != nil {
		t.Fatalf("HookOutput is not the hook envelope: %v", err)
	}
	ctx := env.HookSpecificOutput.AdditionalContext
	if !strings.HasPrefix(ctx, hookContext(rec)) {
		t.Errorf("additionalContext %q does not start with %q", ctx, hookContext(rec))
	}
	if !strings.HasSuffix(ctx, handoffRules) {
		t.Error("additionalContext does not end with the handoff rules")
	}
}
