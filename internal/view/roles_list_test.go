package view

// The FormatRoles tests: FormatRoles lists each role's shape, candidates,
// tier and definitions from the registry, in reg.Names() order.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// TestFormatRolesFileMode pins the file-mode block: the first line carries
// the shape, the gate, the stored tier and roles.json as the source, and a kind
// line carries the definition's agent, its requires and the custom marker.
func TestFormatRolesFileMode(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesViewsCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{MaxTier: "yolo"}, map[string]roles.Row{
		"builder": {
			Tier: ptr("yolo"),
			Definitions: map[string]roles.DefRow{
				"claude": {Agent: "my-executor", Requires: []string{"my-scout"}},
			},
		},
		"reviewer": {},
	})

	got := FormatRoles(reg)

	if !strings.Contains(got, "builder  writer  check  tier yolo  (roles.json)") {
		t.Errorf("first line must carry writer, check, tier yolo and the source:\n%s", got)
	}
	if !strings.Contains(got, "  claude  my-executor + my-scout  (custom)") {
		t.Errorf("kind line must join the requires with + and mark a custom definition:\n%s", got)
	}
	if !strings.Contains(got, "reviewer  reader  tier -  (roles.json)") {
		t.Errorf("a reader role must say reader and carry no check:\n%s", got)
	}
	if strings.Contains(got, "reader  check") {
		t.Errorf("a reader role has no check:\n%s", got)
	}
}

// TestFormatRolesLegacy pins the legacy block: the source reads (legacy),
// the candidates line is the role's order as written, and a role with none says
// so.
func TestFormatRolesLegacy(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
	]`)
	legacy, err := roles.Build(nil, set, orderOf("builder", testClaudeRef))
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	got := FormatRoles(legacy)

	if !strings.Contains(got, "builder  writer  check  tier -  (legacy)") {
		t.Errorf("legacy builder line must carry writer, check and (legacy):\n%s", got)
	}
	if !strings.Contains(got, "  candidates  m") {
		t.Errorf("legacy candidates line must be the role's order:\n%s", got)
	}
	if !strings.Contains(got, "researcher  reader  tier -  (legacy)\n  candidates  (none)") {
		t.Errorf("a role with no candidates must say (none):\n%s", got)
	}
}

// TestFormatRolesLegacyCandidatesFromRanked pins the fix over the
// round-1 fixtures: researcher is served by claude/anthropic/haiku and no order
// names it, so the legacy candidates line lists it, marked (unlisted), rather
// than wrongly saying (none).
func TestFormatRolesLegacyCandidatesFromRanked(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "roles", "testdata", "legacy-candidates.json")
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load(%s): %v", path, err)
	}
	legacy, err := roles.Build(nil, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	got := FormatRoles(legacy)

	if !strings.Contains(got, "researcher  reader  tier -  (legacy)\n  candidates  haiku (unlisted)") {
		t.Errorf("researcher's legacy candidates line must list the unlisted candidate:\n%s", got)
	}
}
