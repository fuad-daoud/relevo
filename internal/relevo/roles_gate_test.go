package relevo

// The roles-gate tests for #374 §5: the #238 gate checks each role's resolved
// definitions, custom ones included, and a role-scoped gate applies only to
// that role.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/view"
)

// rolesGateCandidatesJSON is a claude candidate that can serve both roles and
// an opencode one that can serve builder only; the file's rows, not these
// roles, decide who serves what in file mode.
const rolesGateCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`

// rolesGateReviewerDefs is the shipped definition list the registry resolves
// for claude's reviewer role when a row overrides nothing.
var rolesGateReviewerDefs = []string{"reviewer"}

// defsKey is the (kind, definition list) key a defsRoleChecker answers for.
func defsKey(kind string, definitions []string) string {
	return kind + "\x00" + strings.Join(definitions, ",")
}

// defsRoleChecker is a harness.RoleChecker test double keyed by kind and
// resolved definition list, so a test can say which definitions are missing
// without the fake knowing which role asked. calls counts every Missing call,
// for the cache test.
type defsRoleChecker struct {
	missing map[string][]string
	calls   int
}

func (c *defsRoleChecker) Missing(kind string, definitions []string) []string {
	c.calls++
	return c.missing[defsKey(kind, definitions)]
}

// TestRolesGateCustomBuilderGatesOnlyItsKindAndRole pins §5 flow: a custom
// builder definition missing on claude gates claude for builder only, so the
// builder walk picks opencode while the reviewer pick of claude proceeds.
//
// Mutation check: delete the gatesForRole call at the top of resolveRole and
// the reviewer half fails -- claude is sole reviewer, and the builder-scoped
// gate would refuse it as all-gated.
func TestRolesGateCustomBuilderGatesOnlyItsKindAndRole(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	set := candidateSet(t, rolesGateCandidatesJSON)
	rt.Candidates = set
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {
			Candidates:  []string{testClaudeRef, testOpencodeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "my-executor"}},
		},
		"reviewer": {Candidates: []string{testClaudeRef}},
	})
	rt.Roles = &defsRoleChecker{missing: map[string][]string{
		defsKey("claude", []string{"my-executor"}): {".claude/agents/my-executor.md"},
	}}

	gates := availability.Gates(AvailabilityDeps(rt))
	if len(gates) != 1 {
		t.Fatalf("gates = %+v, want one builder gate for claude", gates)
	}
	if gates[0].Token != testClaudeRef || gates[0].Role != "builder" || gates[0].Kind != availability.RolesMissing {
		t.Fatalf("gate = %+v, want %s roles-missing for builder", gates[0], testClaudeRef)
	}
	if !strings.Contains(gates[0].Note, "agent definitions missing for builder") || !strings.Contains(gates[0].Note, "yourself") {
		t.Errorf("note = %q, want it to name builder and the custom fix", gates[0].Note)
	}

	// The builder walk skips claude (and names claude's custom definition),
	// and picks opencode.
	res, err := resolveRole(rt.Registry, set, gates, "", "builder")
	if err != nil {
		t.Fatalf("resolveRole(builder): %v", err)
	}
	if res.Token() != testOpencodeRef {
		t.Errorf("builder pick = %q, want %q", res.Token(), testOpencodeRef)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Token != testClaudeRef {
		t.Errorf("builder skipped = %+v, want %s", res.Skipped, testClaudeRef)
	}

	// The builder-only gate does not apply to the reviewer pick of claude.
	res, err = resolveRole(rt.Registry, set, gates, "", "reviewer")
	if err != nil {
		t.Fatalf("resolveRole(reviewer): %v", err)
	}
	if res.Token() != testClaudeRef {
		t.Errorf("reviewer pick = %q, want %q", res.Token(), testClaudeRef)
	}
}

// TestRolesGateExplicitPickRefusedOnlyForItsRole pins §4.3's explicit-pick
// half: an explicit claude builder pick is refused by claude's builder gate,
// while the same explicit pick for reviewer succeeds.
func TestRolesGateExplicitPickRefusedOnlyForItsRole(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	set := candidateSet(t, rolesGateCandidatesJSON)
	rt.Candidates = set
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {
			Candidates:  []string{testClaudeRef, testOpencodeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "my-executor"}},
		},
		"reviewer": {Candidates: []string{testClaudeRef}},
	})
	rt.Roles = &defsRoleChecker{missing: map[string][]string{
		defsKey("claude", []string{"my-executor"}): {".claude/agents/my-executor.md"},
	}}
	gates := availability.Gates(AvailabilityDeps(rt))

	_, err := resolveRole(rt.Registry, set, gates, testClaudeRef, "builder")
	if err == nil {
		t.Fatal("explicit claude builder pick succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "agent definitions missing") {
		t.Errorf("err = %q, want it to say agent definitions missing", err.Error())
	}

	res, err := resolveRole(rt.Registry, set, gates, testClaudeRef, "reviewer")
	if err != nil {
		t.Fatalf("explicit claude reviewer pick: %v", err)
	}
	if res.Token() != testClaudeRef {
		t.Errorf("reviewer pick = %q, want %q", res.Token(), testClaudeRef)
	}
}

// TestRolesGateChecksEachDefinitionListOnce pins §4.2's cache: three
// candidates of one kind serving one role share a definition list, so the
// checker is asked once, not three times.
func TestRolesGateChecksEachDefinitionListOnce(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
	  {"harness":"claude","provider":"test","model":"b","roles":["builder"]},
	  {"harness":"claude","provider":"test","model":"c","roles":["builder"]}
	]`)
	rt.Candidates = set
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/a", "claude/test/b", "claude/test/c"}},
	})
	checker := &defsRoleChecker{missing: map[string][]string{}}
	rt.Roles = checker

	if gates := availability.Gates(AvailabilityDeps(rt)); len(gates) != 0 {
		t.Fatalf("gates = %+v, want none", gates)
	}
	if checker.calls != 1 {
		t.Errorf("Missing calls = %d, want 1: one per distinct (kind, definition list)", checker.calls)
	}
}

// TestRolesGateLegacyChecksEachRole pins behaviour change 1: in legacy mode a
// candidate serving reviewer is gated for that role when the reviewer's file is
// missing, and the builder pick of that same candidate is not blocked.
func TestRolesGateLegacyChecksEachRole(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Roles = &defsRoleChecker{missing: map[string][]string{
		defsKey("claude", rolesGateReviewerDefs): {".claude/agents/reviewer.md"},
	}}

	gates := availability.Gates(AvailabilityDeps(rt))
	var roleGates []availability.Gate
	for _, g := range gates {
		if g.Kind == availability.RolesMissing {
			roleGates = append(roleGates, g)
		}
	}
	if len(roleGates) != 1 {
		t.Fatalf("roles-missing gates = %+v, want exactly the reviewer's", roleGates)
	}
	if roleGates[0].Token != testClaudeRef || roleGates[0].Role != "reviewer" {
		t.Errorf("gate = %+v, want %s scoped to reviewer", roleGates[0], testClaudeRef)
	}
	if !strings.Contains(roleGates[0].Note, "agent definitions missing for reviewer") {
		t.Errorf("note = %q, want it to name reviewer", roleGates[0].Note)
	}

	// The builder pick of claude is not blocked by the reviewer's gate.
	res, err := resolveCandidate(rt.Candidates, rt.Policy, gates, testClaudeRef, "builder")
	if err != nil {
		t.Fatalf("explicit claude builder pick: %v", err)
	}
	if res.Token() != testClaudeRef {
		t.Errorf("builder pick = %q, want %q", res.Token(), testClaudeRef)
	}
}

// TestGateRoleJSON pins §3: Role carries `json:"Role,omitempty"`, so an
// unscoped gate keeps every existing status document byte-identical and a
// role-scoped one is visible.
func TestGateRoleJSON(t *testing.T) {
	t.Parallel()

	rep := view.Report{Gated: []availability.Gate{{Token: testClaudeRef, Kind: availability.RolesMissing}}}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), `"Role"`) {
		t.Errorf("unscoped gate marshalled a Role key: %s", raw)
	}

	rep.Gated[0].Role = "builder"
	raw, err = json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"Role":"builder"`) {
		t.Errorf("scoped gate = %s, want a Role key with builder", raw)
	}
}
