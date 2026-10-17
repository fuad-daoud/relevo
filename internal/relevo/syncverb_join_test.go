package relevo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These drive the daemon side of `enable --join` over the in-memory log: a
// machine that has never synced, handed a code and a passphrase. Nothing here
// reaches a network, and every assertion is about what this machine is left
// holding rather than about the method that decided it.

const (
	joinPassphrase = "correct horse battery staple"
	joinToken      = "FIXTURE-JOIN-TOKEN-0123456789abcdef"
	joinRemote     = "libsql://fleet.invalid"
)

// joinMachine is a machine that does not sync yet, pointed at a log holding key
// material derived from joinPassphrase.
//
// The log holds the key rather than the runner holding it, because that is what
// a remote does: the machine sends a passphrase and the remote answers whether it
// matches. A fixture that stored the expected key locally would pass whatever the
// code did with the passphrase, which is the thing being tested.
//
// The log also holds one peer that exported the fleet's bucket credentials, which
// is what a fleet that shares bodies looks like from a joiner. A join that gets
// no credentials at all is refused after the document import, so a fixture with
// no peer cannot exercise anything past the preflight.
func joinMachine(t *testing.T, pass string) (*VerbRunner, relevosync.Local, *synclog.MemTransport) {
	t.Helper()
	runner, local := joinRunner(t)
	log := synclog.NewMemTransport("m1")
	if _, err := log.KeyInit(context.Background(), keyMetaFor(t, pass)); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	exportFleetR2(t, log, pass)
	runner.Open = func(context.Context) (synclog.LogTransport, error) { return log, nil }
	return runner, local, log
}

// fleetOrigin is the installation the fixture fleet writes as. It is a name no
// other fixture in this package uses, so its entries cannot be mistaken for
// another machine's.
const fleetOrigin = "fleet"

// exportFleetR2 runs the real exporter over a real store holding the fleet key
// and the four bucket credentials, so the `doc:secrets` entries on the log are
// the ones a fleet produces rather than a shape this file invented. The peer is
// a machine exporting through the same pipeline the machine under test will
// import, so what the joiner receives is whatever the document actually sends.
func exportFleetR2(t *testing.T, log *synclog.MemTransport, pass string) {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "fleet.db"), db.Options{Origin: fleetOrigin})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	key := relevosync.DeriveKey([]byte(pass), []byte("0123456789abcdef"), relevosync.DefaultKDF)
	if err := relevosync.SetKey(local, key, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey on the fleet: %v", err)
	}
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2 on the fleet: %v", err)
	}
	if _, err := exportOnce(shared, log.OnLog(fleetOrigin)); err != nil {
		t.Fatalf("export from the fleet: %v", err)
	}
}

// keyMetaFor is the remote's salt, parameters and check for one passphrase, made
// by the same derivation the real unlock uses rather than a literal.
func keyMetaFor(t *testing.T, pass string) synclog.KeyMeta {
	t.Helper()
	salt := []byte("0123456789abcdef")
	key := relevosync.DeriveKey([]byte(pass), salt, relevosync.DefaultKDF)
	return synclog.KeyMeta{
		Salt:    salt,
		KDF:     relevosync.DefaultKDF.String(),
		Check:   relevosync.KeyCheck(key),
		Present: true,
	}
}

// joinVerb is the frame a joiner sends: the code's remote and token, the
// passphrase, and the join flag.
func joinVerb(t *testing.T, pass string) *wire.SyncVerb {
	t.Helper()
	return &wire.SyncVerb{
		Verb:          wire.SyncVerbEnable,
		RemoteURL:     joinRemote,
		Join:          true,
		PassphraseLen: len(pass),
	}
}

// localState is every row C6 names, read as the bytes they are on disk. A refused
// join has to leave all four exactly as it found them, and comparing the bytes
// rather than the parsed values is what makes that the claim.
func localState(t *testing.T, l relevosync.Local) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows := []struct {
		name string
		read func() ([]byte, bool, error)
	}{
		{"section", func() ([]byte, bool, error) { return l.ConfigGet(relevosync.SectionSettings) }},
		{"token", func() ([]byte, bool, error) { return l.SecretGet(relevosync.SecretToken) }},
		{"sync.key", func() ([]byte, bool, error) { return l.SecretGet(relevosync.SecretKey) }},
		{"enabled", func() ([]byte, bool, error) { return l.KVGet(relevosync.KeyEnabled) }},
		{"remote_kdf", func() ([]byte, bool, error) { return l.KVGet(relevosync.KeyRemoteKDF) }},
	}
	for _, row := range rows {
		value, present, err := row.read()
		if err != nil {
			t.Fatalf("read %s: %v", row.name, err)
		}
		if present {
			out[row.name] = string(value)
			continue
		}
		out[row.name] = "<absent>"
	}
	return out
}

// The right passphrase joins: the machine ends up holding the remote, the token,
// the key the passphrase derived, and the enabled mark.
func TestJoinWithTheRightPassphraseTurnsTheMachineOn(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if !res.OK {
		t.Fatalf("join refused: (%s) %s", res.Code, res.Message)
	}
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	if settings.RemoteURL != joinRemote {
		t.Errorf("stored remote = %q, want the code's %q", settings.RemoteURL, joinRemote)
	}
	token, ok, err := relevosync.ReadToken(local)
	if err != nil || !ok || string(token) != joinToken {
		t.Errorf("stored token = %q (%v), want the code's token", token, err)
	}
	key, ok, err := relevosync.ReadKey(local)
	if err != nil || !ok {
		t.Fatalf("stored sync key = %q (%v), want the one the passphrase derived", key, err)
	}
	if string(key) != string(relevosync.DeriveKey([]byte(joinPassphrase), []byte("0123456789abcdef"), relevosync.DefaultKDF)) {
		t.Error("the stored key is not the one this passphrase derives")
	}
	if on, err := relevosync.Enabled(local); err != nil || !on {
		t.Errorf("sync is on = %v (%v), want the machine joined", on, err)
	}
}

// A join needs no bucket credentials on a command line: a code has nowhere to
// carry them, and they arrive in the secrets document instead. So the preflight's
// unconditional no-R2 refusal does not apply, and the join gets as far as the
// import on nothing but the code and the passphrase -- which is what this pins,
// because the log's peer is what supplies the credentials. A join against a
// remote with no fleet on it at all is a different case, pinned below.
func TestJoinNeedsNoBucketCredentialsOnTheCommandLine(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if !res.OK {
		t.Fatalf("join refused: (%s) %s", res.Code, res.Message)
	}
	if res.Code == wire.SyncCodeNoR2 {
		t.Fatal("a join was refused for missing bucket credentials, which arrive in the secrets document")
	}
	stored, err := relevosync.ReadR2(local)
	if err != nil {
		t.Fatalf("ReadR2: %v", err)
	}
	if stored != verbFixtureR2 {
		t.Errorf("the joiner's R2 credentials = %+v, want the fleet's", stored)
	}
}

// A remote whose fleet never shared its credentials leaves the joiner with none,
// and a machine that turned sync on without them would read as working and then
// hold every origin it could not fetch. The refusal names both fixes, because
// which one applies is a fact this machine cannot see: the inviter may never
// have configured a bucket, or it may have and the joiner may be pointed
// somewhere else.
func TestAJoinWithNoFleetCredentialsIsRefusedNamingBothFixes(t *testing.T) {
	t.Parallel()
	runner, local := joinRunner(t)
	log := synclog.NewMemTransport("m1")
	if _, err := log.KeyInit(context.Background(), keyMetaFor(t, joinPassphrase)); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	runner.Open = func(context.Context) (synclog.LogTransport, error) { return log, nil }

	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK {
		t.Fatal("a join that received no bucket credentials turned the machine on")
	}
	if res.Code != wire.SyncCodeNoR2 {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeNoR2)
	}
	for _, want := range []string{"--r2-endpoint", "db sync invite"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, res.Message)
		}
	}
	if on, err := relevosync.Enabled(local); err != nil || on {
		t.Errorf("sync is on = %v (%v), want the machine left off", on, err)
	}
}

// A plain enable still refuses without them. The R2 leg was skipped for joins
// only, and a machine that is turning sync on by hand still has to say where its
// bodies go.
func TestAPlainEnableStillNeedsItsBucketCredentials(t *testing.T) {
	t.Parallel()
	runner, local := joinRunner(t)
	res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable, RemoteURL: joinRemote},
		[]byte(joinToken))
	if res.OK || res.Code != wire.SyncCodeNoR2 {
		t.Fatalf("a plain enable = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeNoR2)
	}
	if _, stored, _ := relevosync.ReadToken(local); stored {
		t.Error("a refused plain enable stored a token")
	}
}

// C6: a wrong passphrase stores nothing. Every row the staging touched is put
// back exactly as it was -- including the ones that were absent before, which is
// why this compares bytes rather than parsed values.
func TestAWrongPassphraseLeavesEveryRowExactlyAsItWas(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	before := localState(t, local)

	res := runner.Run(context.Background(), joinVerb(t, "not the passphrase"), []byte(joinToken+"not the passphrase"))
	if res.OK {
		t.Fatal("a join with the wrong passphrase was accepted")
	}
	if res.Code != wire.SyncCodeWrongPassphrase {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeWrongPassphrase)
	}
	after := localState(t, local)
	for _, row := range []string{"section", "token", "sync.key", "enabled", "remote_kdf"} {
		if before[row] != after[row] {
			t.Errorf("%s changed across a refused join:\n before %q\n after  %q", row, before[row], after[row])
		}
	}
}

// The same rule on a machine that already holds rows. Restoring over defaults is
// not restoring: a machine with a stored token and a section must still have
// both, byte for byte, after the join it asked for was refused.
func TestAWrongPassphraseRestoresOverRowsThatAlreadyExisted(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	now := time.Unix(0, 0).UTC()
	if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: "libsql://was-here.invalid"}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := relevosync.SetToken(local, []byte("A-TOKEN-THIS-MACHINE-ALREADY-HAD"), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	before := localState(t, local)

	res := runner.Run(context.Background(), joinVerb(t, "not the passphrase"), []byte(joinToken+"not the passphrase"))
	if res.OK {
		t.Fatal("a join with the wrong passphrase was accepted")
	}
	after := localState(t, local)
	for _, row := range []string{"section", "token", "sync.key", "enabled"} {
		if before[row] != after[row] {
			t.Errorf("%s changed across a refused join:\n before %q\n after  %q", row, before[row], after[row])
		}
	}
}

// A joiner with no passphrase at all is refused before anything is stored. It
// would import every entry and hold none of them: a machine that reads as joined
// while sharing nothing with the fleet.
func TestAJoinWithNoPassphraseIsRefusedBeforeAnythingIsStored(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	before := localState(t, local)

	res := runner.Run(context.Background(), joinVerb(t, ""), []byte(joinToken))
	if res.OK {
		t.Fatal("a join with no passphrase was accepted")
	}
	if after := localState(t, local); after["token"] != before["token"] || after["section"] != before["section"] {
		t.Errorf("a join with no passphrase stored something:\n before %+v\n after  %+v", before, after)
	}
}

// A remote holding no key cannot give a joiner anything it could unlock, so the
// join is refused rather than appearing to work.
func TestAJoinAgainstAKeylessRemoteIsRefused(t *testing.T) {
	t.Parallel()
	runner, local := joinRunner(t)
	runner.Open = func(context.Context) (synclog.LogTransport, error) {
		return synclog.NewMemTransport("m1"), nil
	}
	before := localState(t, local)
	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK || res.Code != wire.SyncCodeNoKDF {
		t.Fatalf("a join against a keyless remote = (ok %v, code %q), want %q", res.OK, res.Code, wire.SyncCodeNoKDF)
	}
	if after := localState(t, local); after["token"] != before["token"] || after["section"] != before["section"] {
		t.Errorf("a refused join stored something:\n before %+v\n after  %+v", before, after)
	}
}

// A plain enable that was handed a passphrase verifies it in the same order and
// by the same rule as a join: the wrong one stores nothing.
func TestAPlainEnableWithTheWrongPassphraseStoresNothing(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	before := localState(t, local)

	wrong := "not the passphrase"
	verb := &wire.SyncVerb{
		Verb:          wire.SyncVerbEnable,
		RemoteURL:     joinRemote,
		PassphraseLen: len(wrong),
		R2Endpoint:    verbFixtureR2.Endpoint,
		R2Bucket:      verbFixtureR2.Bucket,
		R2KeyID:       verbFixtureR2.KeyID,
		R2SecretLen:   len(verbFixtureR2.Secret),
	}
	res := runner.Run(context.Background(), verb, []byte(joinToken+wrong+verbFixtureR2.Secret))
	if res.OK {
		t.Fatal("a plain enable with the wrong passphrase was accepted")
	}
	if res.Code != wire.SyncCodeWrongPassphrase {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeWrongPassphrase)
	}
	after := localState(t, local)
	for _, row := range []string{"section", "token", "sync.key", "enabled"} {
		if before[row] != after[row] {
			t.Errorf("%s changed across a refused enable:\n before %q\n after  %q", row, before[row], after[row])
		}
	}
}

// A plain enable that was handed no passphrase is unchanged: it goes on exactly
// as it did before this flag existed, which is what keeps a scripted enable
// working.
func TestAPlainEnableWithNoPassphraseStillJoins(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	res := runner.Run(context.Background(), &wire.SyncVerb{
		Verb:        wire.SyncVerbEnable,
		RemoteURL:   joinRemote,
		R2Endpoint:  verbFixtureR2.Endpoint,
		R2Bucket:    verbFixtureR2.Bucket,
		R2KeyID:     verbFixtureR2.KeyID,
		R2SecretLen: len(verbFixtureR2.Secret),
	}, []byte(joinToken+verbFixtureR2.Secret))
	if !res.OK {
		t.Fatalf("a plain enable with no passphrase refused: (%s) %s", res.Code, res.Message)
	}
	if on, err := relevosync.Enabled(local); err != nil || !on {
		t.Errorf("sync is on = %v (%v), want the machine joined", on, err)
	}
}

// A fresh machine holds no bucket, and its worker is opened from the stored
// set, so a plain enable with a passphrase must store the bucket it was handed
// before it opens the worker the passphrase is checked against.
func TestAPlainEnableWithAPassphraseOnAFreshMachineOpensWithItsFlags(t *testing.T) {
	t.Parallel()
	runner, local, log := joinMachine(t, joinPassphrase)
	runner.Open = func(context.Context) (synclog.LogTransport, error) {
		stored, err := relevosync.ReadR2(local)
		if err != nil {
			return nil, err
		}
		if !stored.Complete() {
			return nil, relevosync.ErrNoR2
		}
		return log, nil
	}
	verb := &wire.SyncVerb{
		Verb:          wire.SyncVerbEnable,
		RemoteURL:     joinRemote,
		PassphraseLen: len(joinPassphrase),
		R2Endpoint:    verbFixtureR2.Endpoint,
		R2Bucket:      verbFixtureR2.Bucket,
		R2KeyID:       verbFixtureR2.KeyID,
		R2SecretLen:   len(verbFixtureR2.Secret),
	}
	res := runner.Run(context.Background(), verb, []byte(joinToken+joinPassphrase+verbFixtureR2.Secret))
	if !res.OK {
		t.Fatalf("a plain enable with a passphrase on a fresh machine refused: (%s) %s", res.Code, res.Message)
	}
	if on, err := relevosync.Enabled(local); err != nil || !on {
		t.Errorf("sync is on = %v (%v), want the machine enabled", on, err)
	}
}

// A refused passphrase puts the staged bucket back: a fresh machine is left
// holding no credentials, exactly as it was.
func TestAPlainEnableWithTheWrongPassphraseLeavesAFreshMachineWithNoBucket(t *testing.T) {
	t.Parallel()
	runner, local, _ := joinMachine(t, joinPassphrase)
	wrong := "not the passphrase"
	verb := &wire.SyncVerb{
		Verb:          wire.SyncVerbEnable,
		RemoteURL:     joinRemote,
		PassphraseLen: len(wrong),
		R2Endpoint:    verbFixtureR2.Endpoint,
		R2Bucket:      verbFixtureR2.Bucket,
		R2KeyID:       verbFixtureR2.KeyID,
		R2SecretLen:   len(verbFixtureR2.Secret),
	}
	if res := runner.Run(context.Background(), verb, []byte(joinToken+wrong+verbFixtureR2.Secret)); res.OK {
		t.Fatal("a plain enable with the wrong passphrase was accepted")
	}
	stored, err := relevosync.ReadR2(local)
	if err != nil {
		t.Fatalf("ReadR2: %v", err)
	}
	if stored != (relevosync.R2Secrets{}) {
		t.Errorf("a refused enable left a bucket behind: %+v", stored.Missing())
	}
}
