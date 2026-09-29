package availability

import (
	"errors"
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

// testGateKV returns a real t.TempDir() database for a Deps' Gates or Latency
// field.
func testGateKV(t *testing.T) db.KV {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
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

// testDeps builds the Deps the moved tests run against: a temp store, the test
// candidate set, a real gates database and a fixed clock.
func testDeps(t *testing.T) Deps {
	t.Helper()
	gates := testGates(t)
	set := candidateSet(t, testCandidatesJSON)
	reg, err := roles.Build(nil, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return Deps{
		Store:        store.New(t.TempDir()),
		Candidates:   set,
		Gates:        gates,
		Latency:      gates,
		Now:          func() time.Time { return baseTime },
		RoleRegistry: func() *roles.Registry { return reg },
	}
}

// badJSONKV returns invalid bytes for every key, so a reader hits the decode
// failure a hand-edited file used to cause.
type badJSONKV struct{}

func (badJSONKV) KVGet(string) ([]byte, bool, error) { return []byte("not json"), true, nil }
func (badJSONKV) KVPut(string, []byte) error         { return nil }
func (badJSONKV) KVDelete(string) error              { return nil }

// failPutKV delegates to an inner KV but fails KVPut for one key, so one
// record's write can be made to fail while another's succeeds.
type failPutKV struct {
	inner db.KV
	key   string
}

func (k failPutKV) KVGet(key string) ([]byte, bool, error) { return k.inner.KVGet(key) }
func (k failPutKV) KVPut(key string, v []byte) error {
	if key == k.key {
		return errors.New("put failed")
	}
	return k.inner.KVPut(key, v)
}
func (k failPutKV) KVDelete(key string) error { return k.inner.KVDelete(key) }
