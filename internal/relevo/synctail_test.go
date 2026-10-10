package relevo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// joinRunner is a machine that holds no token and no bucket credentials, so
// whatever an enable stores came from the verb.
func joinRunner(t *testing.T) (*VerbRunner, relevosync.Local) {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if _, _, err := db.CompressHistoryOnce(shared, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	log := synclog.NewMemTransport("m1")
	return &VerbRunner{
		Shared: shared,
		Local:  local,
		Path:   shared.Path(),
		Runner: &relevosync.Runner{Local: local},
		Open:   func(context.Context) (synclog.LogTransport, error) { return log, nil },
	}, local
}

func r2Verb(secretLen int) *wire.SyncVerb {
	return &wire.SyncVerb{
		Verb:        wire.SyncVerbEnable,
		RemoteURL:   "libsql://join.invalid",
		R2Endpoint:  verbFixtureR2.Endpoint,
		R2Bucket:    verbFixtureR2.Bucket,
		R2KeyID:     verbFixtureR2.KeyID,
		R2SecretLen: secretLen,
	}
}

// The bucket flags and the secret at the end of the tail are what a machine with
// none stored ends up holding, and the token is the tail without the secret.
func TestEnableStoresTheR2CredentialsTheVerbCarried(t *testing.T) {
	runner, local := joinRunner(t)
	tail := []byte(verbFixtureToken + verbFixtureR2.Secret)
	res := runner.Run(context.Background(), r2Verb(len(verbFixtureR2.Secret)), tail)
	if !res.OK {
		t.Fatalf("enable refused: %s", res.Message)
	}
	got, err := relevosync.ReadR2(local)
	if err != nil {
		t.Fatalf("ReadR2: %v", err)
	}
	if got != verbFixtureR2 {
		t.Errorf("stored R2 = %+v, want %+v", got, verbFixtureR2)
	}
	token, _, err := relevosync.ReadToken(local)
	if err != nil || string(token) != verbFixtureToken {
		t.Errorf("stored token = %q (%v), want the tail without the secret", token, err)
	}
}

func TestEnableWithNoBucketCredentialsIsNoR2(t *testing.T) {
	runner, _ := joinRunner(t)
	res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable, RemoteURL: "libsql://join.invalid"},
		[]byte(verbFixtureToken))
	if res.OK || res.Code != wire.SyncCodeNoR2 {
		t.Fatalf("enable = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeNoR2)
	}
}

// A secret length past the tail is refused before anything is stored, rather
// than clamped into a split that would keep part of the token as the secret.
func TestEnableRefusesASecretLongerThanTheTail(t *testing.T) {
	runner, local := joinRunner(t)
	res := runner.Run(context.Background(), r2Verb(len(verbFixtureToken)+1), []byte(verbFixtureToken))
	if res.OK || res.Code != wire.SyncCodeInvalid {
		t.Fatalf("enable = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeInvalid)
	}
	if _, stored, _ := relevosync.ReadToken(local); stored {
		t.Error("a refused enable stored a token")
	}
}

// pullWatch is a log that records, on each of the join's pulls, whether the
// runner reported a join in progress.
type pullWatch struct {
	*synclog.MemTransport
	runner *VerbRunner
	seen   []bool
}

func (a *pullWatch) Pull(marks map[string]int) ([]synclog.Entry, error) {
	a.seen = append(a.seen, a.runner.Joining())
	return a.MemTransport.Pull(marks)
}

// The daemon stays up for a join by asking the runner, so Joining is true while
// the join drives its log and false once the enable has settled.
func TestJoiningIsTrueOnlyWhileTheJoinRuns(t *testing.T) {
	runner, local := joinRunner(t)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	watch := &pullWatch{MemTransport: synclog.NewMemTransport("m1"), runner: runner}
	runner.Open = func(context.Context) (synclog.LogTransport, error) { return watch, nil }
	if runner.Joining() {
		t.Fatal("Joining before any enable")
	}
	res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable, RemoteURL: "libsql://join.invalid"},
		[]byte(verbFixtureToken))
	if !res.OK {
		t.Fatalf("enable refused: %s", res.Message)
	}
	if len(watch.seen) == 0 || !watch.seen[0] {
		t.Errorf("Joining during the join's pulls = %v, want true", watch.seen)
	}
	if runner.Joining() {
		t.Error("Joining after the enable settled")
	}
	if (*VerbRunner)(nil).Joining() || (*Daemon)(nil).Joining() {
		t.Error("a runner or daemon not yet built reports a join")
	}
}
