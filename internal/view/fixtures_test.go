package view

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// baseTime is a fixed clock the render tests share, so the ages they expect
// never depend on the wall.
var baseTime = time.Unix(1757000000, 0).UTC()

const (
	testOpencodeRef = "opencode/test/m"
	testClaudeRef   = "claude/test/m"
	testAgyRef      = "agy/test/m"
)

// testCandidatesJSON mirrors the shape of the three aliases the default table
// used to ship, plus a reviewer on claude: opencode and agy serve builder
// only; claude serves both.
const testCandidatesJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
]`

// rolesViewsCandidatesJSON is claude a on both roles, claude b and opencode m
// on builder. In file mode the rows, not these roles, decide who serves what.
const rolesViewsCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"b","roles":["builder"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`

// candidateSet loads a candidate set from a JSON body, for tests that need
// a specific configuration without a file in the repo.
func candidateSet(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write candidate set fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("load candidate set fixture: %v", err)
	}
	return set
}

// orderOf builds a Policy ordering role by toks, for tests that need one
// role's order without a policy.json file on disk.
func orderOf(role string, toks ...string) policy.Policy {
	return policy.Policy{Order: map[string][]string{role: toks}}
}

// rolesFileRegistry builds the registry cmd/relevo builds from roles.json,
// from in-memory rows over set.
func rolesFileRegistry(t *testing.T, set *candidate.Set, pol policy.Policy, rows map[string]roles.Row) *roles.Registry {
	t.Helper()
	reg, err := roles.Build(&roles.File{Rows: rows}, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return reg
}

func ptr[T any](v T) *T { return &v }
