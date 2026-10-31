package uplink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	testOpencodeRef = "opencode/test/m"
	testClaudeRef   = "claude/test/m"
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

// forwardClientCandidatesJSON is one candidate named test-model-1 under a
// provider the servers in the forward tests below do not use: the same name
// and model as the server's, on another machine's quota group.
const forwardClientCandidatesJSON = `[
  {"harness":"opencode","provider":"laptop-group","model":"test-model-1","roles":["builder"]}
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

// testGateKV returns a real t.TempDir() database for a Runtime's Gates field.
func testGateKV(t *testing.T) db.KV {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// saveOpenGate saves one open remote round on server.
func saveOpenGate(t *testing.T, st *store.Store, name, server string) {
	t.Helper()
	b := remoteBinding(server)
	b.Name = name
	b.CWD = "/fake/" + name
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}
}

// seedAccountGate writes one live group@account rate-limit entry for provider,
// the local record a `gate <token>` leaves before it forwards.
func seedAccountGate(t *testing.T, rt relevo.Runtime, provider, accountName string) {
	t.Helper()
	err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: []availability.Entry{
		{Kind: availability.RateLimited, Subject: provider + "@" + accountName, At: baseTime, Source: "planner"},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// staleAnswer is the server's own answer for a binding it no longer holds.
func staleAnswer() error {
	return &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound, Message: "not found"}}
}

func callsWithPrefix(fr *fakeRemote, prefix string) []string {
	var out []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func unknownEverywhere() *fakeRemote {
	err := fmt.Errorf("get: %w", client.ErrUnknownServer)
	return &fakeRemote{candidatesErr: err, whoAmIErr: err, unavailableErr: err, availableErr: err}
}
