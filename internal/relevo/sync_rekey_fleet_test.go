package relevo

// The rekey's fleet scenarios, on the in-memory log through the real steady
// pipeline: one machine rekeys while another edits a secret and a third is
// offline, and the fleet converges on the edit; two machines' re-seals of the
// same value converge under the stamps their keys carried; a joiner that
// arrives after the rekey verifies the whole history and reads every secret;
// and a rekey that landed on the remote but not in the rekeyer's local store
// recovers through unlock alone.

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// fleetPass is the fleet's passphrase at epoch 1, and fleetPassNew is what it
// rotates to. Both are derived for real, so every unlock below is a real check.
var (
	fleetPass    = []byte("the fleet's current passphrase")
	fleetPassNew = []byte("the fleet's new passphrase")
)

// newFleetMachine is one enabled machine with no key, whose runner drives log
// under the origin its own file chose.
func newFleetMachine(t *testing.T, log *synclog.MemTransport) *cfgMachine {
	t.Helper()
	return newCfgMachine(t, log, nil, true)
}

// transport is the log view this machine appends and reads under.
func (m *cfgMachine) transport() synclog.LogTransport {
	return m.log.OnLog(m.mdb.Origin())
}

// unlock joins the machine to the fleet's key: isNew creates the material, on
// the machine that founds the fleet.
func (m *cfgMachine) unlock(t *testing.T, pass []byte, isNew bool) {
	t.Helper()
	if _, err := relevosync.Unlock(context.Background(), m.local, m.transport(), pass, isNew, rand.Reader); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

// rekey rotates the log's key through the real act, machine-local store
// included.
func (m *cfgMachine) rekey(t *testing.T, current, newPass []byte) {
	t.Helper()
	if err := relevosync.Rekey(context.Background(), m.local, m.transport(), current, newPass, rand.Reader); err != nil {
		t.Fatalf("Rekey: %v", err)
	}
}

// attemptOut is attempt with the result, for the assertions that count what an
// attempt moved.
func (m *cfgMachine) attemptOut(t *testing.T) relevosync.SteadyResult {
	t.Helper()
	res := m.runner.SyncOnce(context.Background(), m.mdb)
	if res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	return res
}

// holdsNothing asserts the stale stop's shape on one attempt: nothing appended,
// nothing applied, and the one trouble line naming the unlock.
func holdsNothing(t *testing.T, res relevosync.SteadyResult, where string) {
	t.Helper()
	if res.Exported != 0 || res.Applied != 0 {
		t.Fatalf("%s: exported %d, applied %d; a stale machine moves neither half", where, res.Exported, res.Applied)
	}
	if len(res.Trouble.Held) == 0 {
		t.Fatalf("%s: no trouble reported; the stale stop is silent", where)
	}
}

// TestARekeyConvergesOnAPeerEdit pins the three-machine scenario: A rekeys
// while B edits a secret and C is offline; B and C unlock afterwards and the
// fleet ends on B's edit, readable under the newest epoch on every machine --
// with no machine re-emitting anything once it has.
func TestARekeyConvergesOnAPeerEdit(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	b := newFleetMachine(t, log)
	c := newFleetMachine(t, log)
	a.unlock(t, fleetPass, true)
	b.unlock(t, fleetPass, false)
	c.unlock(t, fleetPass, false)

	// The fleet shares one value at epoch 1.
	a.setSecret(t, config.SecretTypesafe, "the value before the rekey")
	a.attempt(t)
	b.attempt(t)
	c.attempt(t)

	// B edits while the rekey lands. Its edit is a person's write, stamped by
	// its own clock; the pause keeps the two clocks' stamps in the order the
	// events happened.
	b.setSecret(t, config.SecretTypesafe, canarySecret)
	time.Sleep(2 * time.Millisecond)
	a.rekey(t, fleetPass, fleetPassNew)

	// A's next attempt re-seals what it holds under the newest epoch and sends
	// it out; the re-seal keeps the stamp the key already carried.
	if res := a.attemptOut(t); res.Exported == 0 {
		t.Fatal("the rekeyed machine exported nothing; the secrets document was never signalled")
	}
	// B and C are on the old key: neither half moves until they unlock.
	holdsNothing(t, b.attemptOut(t), "B while stale")
	holdsNothing(t, c.attemptOut(t), "C while stale")

	// B and C come back with the new passphrase, and the fleet converges on
	// B's edit.
	b.unlock(t, fleetPassNew, false)
	b.attemptOut(t)
	c.unlock(t, fleetPassNew, false)
	c.attemptOut(t)
	a.attemptOut(t)

	for _, m := range []*cfgMachine{a, b, c} {
		if got := m.secret(t, config.SecretTypesafe); got != canarySecret {
			t.Errorf("%s holds %q, want B's edit", m.label(t), got)
		}
	}

	// Converged: another attempt on every machine re-emits nothing, so the
	// fleet is not ping-ponging the same value back and forth.
	for _, m := range []*cfgMachine{a, b, c} {
		if res := m.attemptOut(t); res.Exported != 0 {
			t.Errorf("%s re-exported %d entries on a settled fleet", m.label(t), res.Exported)
		}
	}
}

// TestTwoReSealsConvergeUnderTheKeptStamps pins answer C's convergence: two
// machines holding the same value re-seal it under the newest epoch, each under
// the stamp its key already carried, and the fleet settles on one of them --
// neither machine re-emitting again on later signals.
func TestTwoReSealsConvergeUnderTheKeptStamps(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	b := newFleetMachine(t, log)
	a.unlock(t, fleetPass, true)
	b.unlock(t, fleetPass, false)

	// Both hold the same value, and both export states carry it.
	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)
	b.attempt(t)

	a.rekey(t, fleetPass, fleetPassNew)
	b.unlock(t, fleetPassNew, false)

	// Each machine re-seals its own copy; the entries cross, one rank wins,
	// and the plaintext is the same either way because one plaintext under one
	// epoch seals to one envelope.
	a.attemptOut(t)
	b.attemptOut(t)
	if got := b.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Fatalf("B holds %q after the cross, want the value it started with", got)
	}
	if got := a.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Fatalf("A holds %q after the cross, want the value it started with", got)
	}

	// Settled: neither machine re-emits on the next signals.
	for _, m := range []*cfgMachine{a, b} {
		if res := m.attemptOut(t); res.Exported != 0 {
			t.Errorf("%s re-exported %d entries after the re-seals converged", m.label(t), res.Exported)
		}
	}
}

// TestAJoinerAfterTheRekeyVerifiesTheHistoryAndReadsTheSecrets pins the
// late joiner: a machine that joins after the rekey unwraps the whole chain,
// verifies every entry the log holds -- epoch 1's and epoch 2's -- and reads
// the fleet's secrets out of the document.
func TestAJoinerAfterTheRekeyVerifiesTheHistoryAndReadsTheSecrets(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	a.unlock(t, fleetPass, true)
	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.setSecret(t, relevosync.SecretR2Endpoint, "https://acct.r2.cloudflarestorage.com")
	a.setSecret(t, relevosync.SecretR2Bucket, "the-fleet-bucket")
	a.setSecret(t, relevosync.SecretR2KeyID, "the-fleet-key-id")
	a.setSecret(t, relevosync.SecretR2Secret, "the-fleet-r2-secret")
	a.attempt(t)
	a.rekey(t, fleetPass, fleetPassNew)
	a.attemptOut(t)

	// A joiner: not enabled, no key, nothing but the code's remote and token
	// and the fleet's new passphrase. The origin gate runs first, the way a
	// real join's preflight does.
	d := newCfgMachine(t, log, nil, false)
	if _, _, err := db.CompressHistoryOnce(d.mdb, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	runner := &VerbRunner{
		Shared: d.mdb,
		Local:  d.local,
		Path:   d.mdb.Path(),
		Runner: &relevosync.Runner{Local: d.local},
		Open:   func(context.Context) (synclog.LogTransport, error) { return d.transport(), nil },
	}
	verb := &wire.SyncVerb{
		Verb:          wire.SyncVerbEnable,
		RemoteURL:     joinRemote,
		Join:          true,
		PassphraseLen: len(fleetPassNew),
	}
	res := runner.Run(context.Background(), verb, []byte(cfgToken+string(fleetPassNew)))
	if !res.OK {
		t.Fatalf("join refused: (%s) %s", res.Code, res.Message)
	}

	ring, ok, err := relevosync.ReadKeyring(d.local)
	if err != nil || !ok || ring.Epoch != 2 || len(ring.Keys) != 2 {
		t.Fatalf("the joiner's ring = %+v, (ok %v, err %v); want the whole epoch-2 chain", ring, ok, err)
	}
	if got := d.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Errorf("the joiner's typesafe = %q, want the fleet's value", got)
	}
	if got := d.secret(t, relevosync.SecretR2Bucket); got != "the-fleet-bucket" {
		t.Errorf("the joiner's bucket = %q, want the fleet's value", got)
	}
}

// TestARekeyTheRemoteHoldsButTheStoreDoesNotRecoversThroughUnlock pins the
// interruption: the rekey's write landed on the remote and the rekeyer's local
// store never happened, and the owner's unlock with the new passphrase alone
// puts the machine back -- no repair, no rekey of its own.
func TestARekeyTheRemoteHoldsButTheStoreDoesNotRecoversThroughUnlock(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	b := newFleetMachine(t, log)
	a.unlock(t, fleetPass, true)
	b.unlock(t, fleetPass, false)
	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)
	b.attempt(t)

	// The rekey lands on the remote -- the transport's own act -- and the
	// crash eats everything after it: no keyring, no facts, no signal.
	rekeyOnTheLogOnly(t, a, fleetPassNew)

	// The rekeyer is the ordinary stale machine its own decision 4 describes:
	// both halves stop, and the trouble line names the unlock.
	holdsNothing(t, a.attemptOut(t), "the crashed rekeyer")
	if state, err := relevosync.KeyState(a.local); err != nil || state != "stale" {
		t.Fatalf("KeyState = %q, %v; want stale", state, err)
	}

	// The owner unlocks with the new passphrase -- through the verb, which is
	// the whole of the recovery: keyring, own cut, parked docs, join tail.
	runner := &VerbRunner{
		Shared: a.mdb,
		Local:  a.local,
		Path:   a.mdb.Path(),
		Runner: &relevosync.Runner{Local: a.local},
		Open:   func(context.Context) (synclog.LogTransport, error) { return a.transport(), nil },
	}
	if res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbUnlock}, fleetPassNew); !res.OK {
		t.Fatalf("unlock refused: (%s) %s", res.Code, res.Message)
	}
	a.attemptOut(t)
	if got := a.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Errorf("A holds %q after the recovery, want the value it rekeyed away from", got)
	}
	if state, err := relevosync.KeyState(a.local); err != nil || state != "unlocked" {
		t.Errorf("KeyState = %q, %v; want unlocked after the recovery", state, err)
	}
	b.attemptOut(t)
}

// rekeyOnTheLogOnly performs the remote half of a rekey and none of the local
// one, the state a crash between the two transactions leaves.
func rekeyOnTheLogOnly(t *testing.T, m *cfgMachine, newPass []byte) {
	t.Helper()
	ring, ok, err := relevosync.ReadKeyring(m.local)
	if err != nil || !ok {
		t.Fatalf("ReadKeyring = (ok %v, err %v)", ok, err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("salt: %v", err)
	}
	newKey := relevosync.DeriveKey(newPass, salt, relevosync.DefaultKDF)
	wrapped, err := relevosync.WrapKey(newKey, 1, ring.Keys[1])
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	state, err := m.log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	cuts := make(map[string]synclog.EpochCut, len(state.Heads))
	for origin, head := range state.Heads {
		cuts[origin] = synclog.EpochCut{Epoch: 2, Seq: head, Tag: synclog.EpochCutTag(newKey, origin, 2, head)}
	}
	if _, err := m.transport().KeyRekey(context.Background(), synclog.KeyRekeySpec{
		From: 1,
		Key: synclog.KeyMeta{
			Salt: salt, KDF: relevosync.DefaultKDF.String(), Check: relevosync.KeyCheck(newKey),
			Present: true, Epoch: 2, Wrapped: map[int][]byte{1: wrapped},
		},
		Cuts: cuts,
	}); err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
}
