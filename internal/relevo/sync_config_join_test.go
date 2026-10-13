package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// join enables sync on a machine that has not joined yet.
func (m *cfgMachine) join(t *testing.T) {
	t.Helper()
	if _, _, err := db.CompressHistoryOnce(m.mdb, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	if err := relevosync.SetR2(m.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	enabler := &relevosync.Enabler{
		Request: relevosync.EnableRequest{
			Local: m.local, Shared: m.mdb, URL: "libsql://join.invalid", Token: []byte(cfgToken),
		},
		Transport: m.log.OnLog(m.mdb.Origin()),
		Now:       func() time.Time { return time.Unix(0, 0).UTC() },
	}
	if _, err := enabler.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
}

func (m *cfgMachine) syncRevisions(t *testing.T) int {
	t.Helper()
	n := 0
	for _, s := range m.sources(t) {
		if strings.HasPrefix(s, "sync:") {
			n++
		}
	}
	return n
}

// seededJoinPair is A with an edit of candidate "m" exported at base, and a
// later-created B that stamped its own different "m" afterwards, not yet joined.
func seededJoinPair(t *testing.T, key []byte) (a, b *cfgMachine) {
	t.Helper()
	base := time.Now().Add(-time.Hour).UTC()
	log := synclog.NewMemTransport("x")
	a = newCfgMachine(t, log, cfgSyncKey, true)
	b = newCfgMachine(t, log, key, false)
	a.setAt(t, base, config.Candidates, "["+cfgCandidateNamed("m", "shared-from-a")+"]")
	a.export(t)
	b.setAt(t, base.Add(time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "seed-from-b")+"]")
	return a, b
}

func TestJoinAdoptsTheSharedConfigOverItsSeed(t *testing.T) {
	t.Parallel()
	a, b := seededJoinPair(t, cfgSyncKey)
	before := a.syncRevisions(t)

	b.join(t)
	b.attempt(t)
	a.attempt(t)

	assertConverged(t, a, b, "shared-from-a")
	if got := a.syncRevisions(t); got != before {
		t.Errorf("A gained %d sync revisions, want none", got-before)
	}
}

func TestLockedJoinerAdoptsTheSharedConfigOnUnlock(t *testing.T) {
	t.Parallel()
	a, b := seededJoinPair(t, nil)
	before := a.syncRevisions(t)

	b.join(t)
	if err := relevosync.SetKey(b.local, cfgSyncKey, time.Now().UTC()); err != nil {
		t.Fatalf("unlock B: %v", err)
	}
	if _, err := relevosync.DrainParkedDocs(b.mdb, b.local, b.log.OnLog(b.mdb.Origin())); err != nil {
		t.Fatalf("DrainParkedDocs: %v", err)
	}
	b.attempt(t)
	a.attempt(t)

	assertConverged(t, a, b, "shared-from-a")
	if got := a.syncRevisions(t); got != before {
		t.Errorf("A gained %d sync revisions, want none", got-before)
	}
}

func TestJoinerKeepsKeysOnlyItHas(t *testing.T) {
	t.Parallel()
	a, b := seededJoinPair(t, cfgSyncKey)
	b.setAt(t, time.Now().UTC(), config.Candidates,
		"["+cfgCandidateNamed("m", "seed-from-b")+","+cfgCandidateNamed("only-b", "mine")+"]")

	b.join(t)
	b.attempt(t)
	a.attempt(t)

	if got := a.body(t, config.Candidates); !strings.Contains(got, "only-b") {
		t.Errorf("A holds %s, want the candidate only B had", got)
	}
	if got := a.body(t, config.Candidates); !strings.Contains(got, "shared-from-a") {
		t.Errorf("A holds %s, want its own shared value kept", got)
	}
}
