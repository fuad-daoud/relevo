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
	if err := relevosync.SetR2(m.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	m.joinWith(t, false, m.log.OnLog(m.mdb.Origin()))
}

// joinWith runs the enable over transport, as a join when joining is set. The
// two are not the same path: a join clears the doc export state and its write
// stamps before the import, so a value it adopted is never re-emitted as its
// own edit. A machine joining with a code has to take that path or it answers
// the fleet's values straight back.
func (m *cfgMachine) joinWith(t *testing.T, joining bool, transport synclog.LogTransport) {
	t.Helper()
	if _, _, err := db.CompressHistoryOnce(m.mdb, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	enabler := &relevosync.Enabler{
		Request: relevosync.EnableRequest{
			Local: m.local, Shared: m.mdb, URL: "libsql://join.invalid", Token: []byte(cfgToken),
			Join: joining,
		},
		Transport: transport,
		Now:       func() time.Time { return time.Unix(0, 0).UTC() },
	}
	if _, err := enabler.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
}

// syncRevisions counts the revisions a value arriving from a peer left. The
// secrets document's revisions are not counted: a joiner is owed the fleet's R2
// credentials, so a peer adopting them is the point rather than an echo, and the
// property these tests pin is about the config sections, which is where an echo
// would be visible.
func (m *cfgMachine) syncRevisions(t *testing.T) int {
	t.Helper()
	rows, err := m.cfg.Log(0)
	if err != nil {
		t.Fatalf("config log: %v", err)
	}
	n := 0
	for _, r := range rows {
		if !strings.HasPrefix(r.Source, "sync:") {
			continue
		}
		if strings.Contains(string(r.Changes), "secret.") {
			continue
		}
		n++
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
