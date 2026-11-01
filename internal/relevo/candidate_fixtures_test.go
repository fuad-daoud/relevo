package relevo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
)

const (
	testOpencodeRef = "opencode/test/m"
	testClaudeRef   = "claude/test/m"
	testAgyRef      = "agy/test/m"
)

// testCandidatesJSON is three candidates: opencode and agy serve builder
// only; claude serves builder and reviewer.
const testCandidatesJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
]`

// testTwoProviderJSON has builders on two providers, so a rate limit on
// one leaves the other ungated.
const testTwoProviderJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"other","model":"m","roles":["builder"]}
]`

// testTwoReviewerJSON: claude and opencode serve reviewer; agy does not.
const testTwoReviewerJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"]}
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

// fakeRoleChecker is a harness.RoleChecker test double: Missing(kind) looks
// up kind in the map, returning nil (present) for any kind not listed. It
// answers the same way whatever definitions are asked for.
type fakeRoleChecker map[string][]string

func (f fakeRoleChecker) Missing(kind string, _ []string) []string { return f[kind] }
