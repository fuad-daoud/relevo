package availability

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// baseTime is the instant every moved test's fixed clock reports.
var baseTime = time.Unix(1757000000, 0).UTC()

const (
	testOpencodeRef = "opencode/test/m"
	testClaudeRef   = "claude/test/m"
	testAgyRef      = "agy/test/m"
	// testOtherProviderRef is the token testTwoProviderJSON gives the agy
	// harness, whose provider is not test: a rate limit on that provider gates
	// it and leaves both test-provider candidates alone.
	testOtherProviderRef = "agy/other/m"
)

// testCandidatesJSON mirrors the shape of the three aliases DefaultTable used
// to ship, plus a reviewer on claude, so migrated tests keep their meaning:
// opencode and agy serve builder only; claude serves both.
const testCandidatesJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
]`

// testTwoProviderJSON has builders on two providers, so a rate limit on one
// leaves the other ungated.
const testTwoProviderJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"other","model":"m","roles":["builder"]}
]`

// candidateSet loads a candidate set from a JSON body, for tests that need a
// specific configuration without a file in the repo.
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

// testGates returns a Gates handle over a real temp database.
func testGates(t *testing.T) db.KV {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// testShared is the shared file a Deps writes its history rows into and reads
// them back from. It is a second file beside the gates one on purpose, because
// it is a second file in production: the rows travel through the shared file's
// outbox while the ledger lives in the machine-local one.
func testShared(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// testDeps builds the Deps the moved tests run against: a temp store, the test
// candidate set, a real gates database, a real shared file and a fixed clock.
func testDeps(t *testing.T) Deps {
	t.Helper()
	set := candidateSet(t, testCandidatesJSON)
	reg, err := roles.Build(nil, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return Deps{
		Store:        store.New(t.TempDir()),
		Candidates:   set,
		Gates:        testGates(t),
		History:      testShared(t),
		Now:          func() time.Time { return baseTime },
		RoleRegistry: func() *roles.Registry { return reg },
	}
}

// testDepsWith is testDeps against a caller's own candidate set, for the tests
// that need a specific configuration rather than the shared three-ref one.
func testDepsWith(t *testing.T, set *candidate.Set) Deps {
	t.Helper()
	reg, err := roles.Build(nil, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return Deps{
		Store:        store.New(t.TempDir()),
		Candidates:   set,
		Gates:        testGates(t),
		History:      testShared(t),
		Now:          func() time.Time { return baseTime },
		RoleRegistry: func() *roles.Registry { return reg },
	}
}

// badJSONKV returns invalid bytes for every key, so a reader hits the decode
// failure a hand-edited file causes.
type badJSONKV struct{}

func (badJSONKV) KVGet(string) ([]byte, bool, error) { return []byte("not json"), true, nil }
func (badJSONKV) KVPut(string, []byte) error         { return nil }
func (badJSONKV) KVDelete(string) error              { return nil }
