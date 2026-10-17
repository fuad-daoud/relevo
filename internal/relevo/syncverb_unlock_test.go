package relevo

// The unlock verb and the secrets a machine held while it was locked: unlocking
// is what lets them travel, and it must not let an older value win over a
// peer's newer one.

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// unlockVerb unlocks the machine through the verb a person runs, which is
// where the steps that follow the key's storing live.
func (m *cfgMachine) unlockVerb(t *testing.T, isNew bool) {
	t.Helper()
	runner := &VerbRunner{
		Shared: m.mdb,
		Local:  m.local,
		Path:   m.mdb.Path(),
		Runner: m.runner,
		Open:   func(context.Context) (synclog.LogTransport, error) { return m.transport(), nil },
	}
	verb := &wire.SyncVerb{Verb: wire.SyncVerbUnlock, New: isNew}
	if res := runner.Run(context.Background(), verb, fleetPass); !res.OK {
		t.Fatalf("unlock refused: %s (%s)", res.Message, res.Code)
	}
}

// heldBeforeTheDocument stores R2 credentials the way a machine that configured
// them before the secrets document existed holds them: no write stamp and no
// signal on the document.
func heldBeforeTheDocument(t *testing.T, m *cfgMachine, r2 relevosync.R2Secrets) {
	t.Helper()
	if err := relevosync.SetR2(m.local, r2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	r2NamesWithoutStamping(t, m)
	if err := m.mdb.LocalOrSelf().Tx(func(tx *db.Tx) error { return tx.ClearDocSignals(config.Secrets) }); err != nil {
		t.Fatalf("ClearDocSignals: %v", err)
	}
}

// TestUnlockSendsTheSecretsHeldWhileLocked pins the route a fleet's first key
// opens: credentials stored before any machine held a key travel on the first
// export after the unlock, and a peer that unlocks with the same passphrase
// receives them.
func TestUnlockSendsTheSecretsHeldWhileLocked(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	b := newFleetMachine(t, log)
	heldBeforeTheDocument(t, a, verbFixtureR2)

	a.unlockVerb(t, true)
	a.attempt(t)

	state, err := a.mdb.DocExportState(config.Secrets)
	if err != nil {
		t.Fatalf("DocExportState: %v", err)
	}
	for _, name := range []string{relevosync.SecretR2Endpoint, relevosync.SecretR2Bucket, relevosync.SecretR2KeyID, relevosync.SecretR2Secret} {
		if _, ok := state[name]; !ok {
			t.Errorf("%s was not exported after the unlock", name)
		}
	}

	b.unlockVerb(t, false)
	b.attempt(t)
	got, err := relevosync.ReadR2(b.local)
	if err != nil {
		t.Fatalf("ReadR2 on B: %v", err)
	}
	if got != verbFixtureR2 {
		t.Errorf("B's R2 credentials = %+v, want A's", got)
	}
}

// TestALateUnlockDoesNotSendAnOlderSecretOverANewerOne pins the order inside
// the verb: a machine that held an older value while locked applies the
// peer's newer one before its own export runs, so the older value never takes
// a fresh stamp and wins.
func TestALateUnlockDoesNotSendAnOlderSecretOverANewerOne(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	b := newFleetMachine(t, log)
	older := verbFixtureR2
	older.Secret = "the secret before the rotation"
	heldBeforeTheDocument(t, b, older)

	a.unlockVerb(t, true)
	if err := relevosync.SetR2(a.local, verbFixtureR2, time.Now().UTC()); err != nil {
		t.Fatalf("SetR2 on A: %v", err)
	}
	a.attempt(t)
	b.attempt(t) // locked: parks A's secrets entries

	b.unlockVerb(t, false)
	b.attempt(t)
	a.attempt(t)

	for _, m := range []*cfgMachine{a, b} {
		got, err := relevosync.ReadR2(m.local)
		if err != nil {
			t.Fatalf("ReadR2: %v", err)
		}
		if got.Secret != verbFixtureR2.Secret {
			t.Errorf("a machine holds the R2 secret %q, want the newer %q", got.Secret, verbFixtureR2.Secret)
		}
	}
}
