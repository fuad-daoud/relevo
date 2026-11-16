package verb

// The none-history joins and pulls: a joiner holding only the code gets the
// fleet's R2 credentials through ranged reads, and a one-shot pull on a none
// machine mirrors instead of importing. Every case runs against the in-memory
// log, so no case reaches a network or spawns a worker.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// countingJoinLog is the fleet log with a count per call, so a case pins which
// half of the pull ran: the ranged pre-pass, the whole-log import, or neither.
type countingJoinLog struct {
	*synclog.MemTransport
	pulls   atomic.Int64
	ranges  atomic.Int64
	appends atomic.Int64
}

func (l *countingJoinLog) Pull(marks map[string]int) ([]synclog.Entry, error) {
	l.pulls.Add(1)
	return l.MemTransport.Pull(marks)
}

func (l *countingJoinLog) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	l.ranges.Add(1)
	return l.MemTransport.PullRange(origin, after, upto)
}

func (l *countingJoinLog) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	l.appends.Add(1)
	return l.MemTransport.Append(entries)
}

// noneJoinFixture is a joiner holding only the code: no token, no bucket
// credentials, and the fleet on a counted log. Both openers hand back the same
// log, so every transport call of the join lands on one count.
func noneJoinFixture(t *testing.T, history string) (*Runner, relevosync.Local, *countingJoinLog) {
	t.Helper()
	runner, local := joinRunner(t)
	log := synclog.NewMemTransport("m1")
	if _, err := log.KeyInit(context.Background(), keyMetaFor(t, joinPassphrase)); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	exportFleetR2(t, log, joinPassphrase)
	fake := &countingJoinLog{MemTransport: log}
	runner.OpenNoBucket = func(context.Context) (synclog.LogTransport, bool, error) { return fake, true, nil }
	runner.Open = func(context.Context) (synclog.LogTransport, error) { return fake, nil }
	return runner, local, fake
}

// TestJoinNoneAndFullStoreFleetCredentials pins the join's none routing: with
// the section at none the join serves the secrets document from ranged reads
// only, never the whole-log import, and the credentials end up stored all the
// same. The full-history join is the control: it still pulls.
func TestJoinNoneAndFullStoreFleetCredentials(t *testing.T) {
	t.Parallel()
	for _, history := range []string{relevosync.HistoryNone, relevosync.HistoryFull} {
		t.Run("history="+history, func(t *testing.T) {
			t.Parallel()
			runner, local, fake := noneJoinFixture(t, history)
			verb := joinVerb(t, joinPassphrase)
			verb.History = history

			res := runner.Run(context.Background(), verb, []byte(joinToken+joinPassphrase))
			if !res.OK {
				t.Fatalf("join refused: (%s) %s", res.Code, res.Message)
			}
			stored, err := relevosync.ReadR2(local)
			if err != nil {
				t.Fatalf("ReadR2: %v", err)
			}
			if stored != verbFixtureR2 {
				t.Errorf("stored R2 = %+v, want the fleet's set", stored)
			}
			if got := fake.ranges.Load(); got == 0 {
				t.Error("PullRange never ran, want the pre-pass's ranged reads")
			}
			if got := fake.appends.Load(); got != 0 {
				t.Errorf("Append ran %d times, want a join that stores nothing of its own", got)
			}
			if history == relevosync.HistoryNone {
				if got := fake.pulls.Load(); got != 0 {
					t.Errorf("Pull ran %d times, want none: a none join never imports", got)
				}
			} else if got := fake.pulls.Load(); got == 0 {
				t.Error("Pull never ran, want the full import on a full-history join")
			}
		})
	}
}

// nonePullFixture is a machine pointed at the fleet, holding the fleet's key
// and nothing else: no token, no bucket credentials and no write stamps of its
// own, so the fleet's secrets document is newer than anything here. A pull
// needs no token, and the client is installed, so no remote is dialled.
func nonePullFixture(t *testing.T, history string) (*Runner, relevosync.Local, *db.DB, *countingJoinLog) {
	t.Helper()
	shared, err := db.OpenSplit(t.TempDir()+"/relevo.db", db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.SetKey(local,
		relevosync.DeriveKey([]byte(joinPassphrase), []byte("0123456789abcdef"), relevosync.DefaultKDF),
		time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := relevosync.PutSettings(local,
		relevosync.Settings{History: history}, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	fleet := synclog.NewMemTransport("m1")
	if _, err := fleet.KeyInit(context.Background(), keyMetaFor(t, joinPassphrase)); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	exportFleetR2(t, fleet, joinPassphrase)
	fake := &countingJoinLog{MemTransport: fleet}
	runner := &Runner{
		Shared: shared,
		Local:  local,
		Path:   shared.Path(),
		Runner: &relevosync.Runner{Client: fake, Local: local},
	}
	return runner, local, shared, fake
}

// TestPullNoneMirrorsInsteadOfImporting pins the one-shot's none routing: the
// pull serves the secrets document from ranged reads only, the credentials end
// up stored, and no import mark moves. The full-history pull is the control:
// it still pulls and reports what it applied.
func TestPullNoneMirrorsInsteadOfImporting(t *testing.T) {
	t.Parallel()
	for _, history := range []string{relevosync.HistoryNone, relevosync.HistoryFull} {
		t.Run("history="+history, func(t *testing.T) {
			t.Parallel()
			runner, local, shared, fake := nonePullFixture(t, history)

			res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil)
			if !res.OK {
				t.Fatalf("pull refused: (%s) %s", res.Code, res.Message)
			}
			if history == relevosync.HistoryNone {
				if got := fake.pulls.Load(); got != 0 {
					t.Errorf("Pull ran %d times, want none: a none pull never imports", got)
				}
				if got := fake.ranges.Load(); got == 0 {
					t.Error("PullRange never ran, want the pre-pass's ranged reads")
				}
				if res.Applied {
					t.Error("the pull reports applied, want nothing: the pre-pass count includes redeliveries")
				}
				if marks, err := shared.ImportMarks(); err != nil {
					t.Fatalf("ImportMarks: %v", err)
				} else if len(marks) != 0 {
					t.Errorf("ImportMarks = %v, want none moved", marks)
				}
			} else {
				if got := fake.pulls.Load(); got == 0 {
					t.Error("Pull never ran, want the full import on a full-history pull")
				}
				if !res.Applied {
					t.Error("the pull reports nothing applied, want the secrets document")
				}
			}
			stored, err := relevosync.ReadR2(local)
			if err != nil {
				t.Fatalf("ReadR2: %v", err)
			}
			if stored != verbFixtureR2 {
				t.Errorf("stored R2 = %+v, want the fleet's set", stored)
			}
		})
	}
}

// TestEnableNoneThenFullIsAlreadyEnabled pins that a mode flip needs a
// re-enable: after an enable with none, a second enable with full is refused
// and the section still says none.
func TestEnableNoneThenFullIsAlreadyEnabled(t *testing.T) {
	t.Parallel()
	runner, local := joinRunner(t)
	ctx := context.Background()
	enable := func(history string) *wire.SyncResult {
		t.Helper()
		verb := &wire.SyncVerb{
			Verb: wire.SyncVerbEnable, RemoteURL: "libsql://join.invalid", History: history,
			R2Endpoint: verbFixtureR2.Endpoint, R2Bucket: verbFixtureR2.Bucket,
			R2KeyID: verbFixtureR2.KeyID, R2SecretLen: len(verbFixtureR2.Secret),
		}
		return runner.Run(ctx, verb, []byte(verbFixtureToken+verbFixtureR2.Secret))
	}

	if res := enable(relevosync.HistoryNone); !res.OK {
		t.Fatalf("enable with none refused: (%s) %s", res.Code, res.Message)
	}
	if st, err := relevosync.ReadSettings(local); err != nil {
		t.Fatalf("ReadSettings: %v", err)
	} else if !st.HistoryNone() {
		t.Errorf("section history = %q, want none", st.History)
	}

	res := enable(relevosync.HistoryFull)
	if res.OK || res.Code != wire.SyncCodeAlreadyEnabled {
		t.Fatalf("second enable = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeAlreadyEnabled)
	}
	if st, err := relevosync.ReadSettings(local); err != nil {
		t.Fatalf("ReadSettings: %v", err)
	} else if !st.HistoryNone() {
		t.Errorf("section history = %q after a refused flip, want none", st.History)
	}
}
