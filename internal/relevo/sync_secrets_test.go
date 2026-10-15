package relevo

// These tests drive the secrets document through the real steady pipeline on the
// in-memory log: nothing here binds a document by hand, so a construction site
// that forgets to bind it fails, and every assertion is about the value the peer
// ends up holding rather than about the document's own methods.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/remote"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// canarySecret is the plaintext these tests watch for. It is a value nothing
// else in the tree holds, so finding it anywhere means the sealing was bypassed.
const canarySecret = "CANARY-3f9a-plaintext-that-must-never-travel"

// setSecret is `relevo config secret set`: a person's edit, which stamps.
func (m *cfgMachine) setSecret(t *testing.T, name, value string) {
	t.Helper()
	if err := m.cfg.As("cli", "config secret set "+name).PutSecret(name, []byte(value)); err != nil {
		t.Fatalf("config secret set %s: %v", name, err)
	}
}

// secret is what this machine holds under a name.
func (m *cfgMachine) secret(t *testing.T, name string) string {
	t.Helper()
	value, ok, err := m.cfg.Secret(name)
	if err != nil {
		t.Fatalf("Secret(%s): %v", name, err)
	}
	if !ok {
		return ""
	}
	return string(value)
}

// TestSteadyAttemptCarriesASharedSecretToAPeer is the whole point of the
// document: one machine's typesafe key, set by a person, reaches the peer and
// the peer holds the plaintext.
func TestSteadyAttemptCarriesASharedSecretToAPeer(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)
	b.attempt(t)

	if got := b.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Errorf("B's typesafe = %q, want A's value", got)
	}
}

// TestTheLogCarriesTheSealedValueNotThePlaintext pins that what crosses the wire
// is ciphertext. A peer that could read the log would otherwise hold every
// machine's credentials without ever unlocking anything.
func TestTheLogCarriesTheSealedValueNotThePlaintext(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)
	b.attempt(t)

	entries := wholeLog(t, log)
	found := 0
	for _, e := range entries {
		if strings.Contains(string(e.Body), canarySecret) {
			t.Errorf("an entry on the log carries the plaintext: %s", e.Body)
		}
		if e.Table == "doc:"+config.Secrets {
			found++
		}
	}
	if found == 0 {
		t.Error("no entry on the log names the secrets document")
	}
}

// TestAMaximumSizeSecretTravelsInline pins that the largest secret the cap
// allows reaches the log as bytes rather than as a reference to a body in the
// object store. A machine that has just joined holds no object-store
// credentials until this very document has been applied, so a secret that
// needed a fetch could not be the thing that grants the fetch.
//
// The value is over the blob threshold and incompressible, so it cannot become a
// reference by accident of size on the far side of a codec, and the entry is
// checked for the tag rather than for its length: a reference is a shape, and
// the shape is what a later change to the blob rules would introduce.
func TestAMaximumSizeSecretTravelsInline(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	// The largest value that seals under the cap, found by sealing rather than
	// by arithmetic: the encoding grows faster than the plaintext on some
	// lengths. It is several times the blob threshold either way, so the only
	// thing that could move it into the object store is the blob rules
	// themselves.
	secret := largestSealedSecret(t)
	if !synclog.NeedsRef(secret) {
		t.Fatalf("the fixture is %d bytes and does not exceed the %d byte blob threshold, so it proves nothing",
			len(secret), synclog.BlobRefThreshold)
	}
	a.setSecret(t, relevosync.SecretR2Secret, string(secret))
	a.attempt(t)

	found := 0
	for _, e := range wholeLog(t, log) {
		if e.Table != "doc:"+config.Secrets {
			continue
		}
		found++
		if strings.Contains(string(e.Body), "$ref") {
			t.Errorf("an over-threshold secret travels as a body reference:\n%s", e.Body)
		}
	}
	if found == 0 {
		t.Fatal("no entry names the secrets document, so nothing was exported")
	}
	b.attempt(t)
	if got := b.secret(t, relevosync.SecretR2Secret); len(got) != len(secret) {
		t.Errorf("B holds %d bytes of the secret, want %d", len(got), len(secret))
	}
}

// largestSealedSecret is the biggest value that still seals under the cap.
func largestSealedSecret(t *testing.T) []byte {
	t.Helper()
	for n := relevosync.SecretEnvelopeMax; n > 0; n-- {
		candidate := make([]byte, n)
		for i := range candidate {
			candidate[i] = byte('a' + i%26)
		}
		if _, err := relevosync.SealSecret(cfgSyncKey, relevosync.SecretR2Secret, candidate); err == nil {
			return candidate
		}
	}
	t.Fatal("no value under the cap sealed")
	return nil
}

// TestAnOverCapSecretIsRefusedOnExport pins the cap at the pipeline level. The
// attempt fails rather than exporting a truncated or referenced value, and the
// signal stands so the refusal is reported again rather than swallowed.
func TestAnOverCapSecretIsRefusedOnExport(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)

	over := make([]byte, relevosync.SecretEnvelopeMax)
	a.setSecret(t, relevosync.SecretR2Secret, string(over))
	res := a.runner.SyncOnce(context.Background(), a.mdb)
	if res.Err == nil {
		t.Fatal("an over-cap secret exported; the cap is not binding")
	}
	if !strings.Contains(res.Err.Error(), "cap") {
		t.Errorf("the refusal does not name the cap: %v", res.Err)
	}
	if strings.Contains(res.Err.Error(), string(over[:64])) {
		t.Error("the refusal carries the value")
	}
	signals, err := a.mdb.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	signalled := false
	for _, s := range signals {
		if s.Doc == config.Secrets {
			signalled = true
		}
	}
	if !signalled {
		t.Error("the refused export cleared the document's signal, so the value would never travel")
	}
}

// signalSecrets raises the document's signal by hand, which is what a write
// that is a person's edit does and an unstamped one deliberately does not.
func (m *cfgMachine) signalSecrets(t *testing.T) {
	t.Helper()
	if err := m.mdb.SignalDoc(config.Secrets); err != nil {
		t.Fatalf("SignalDoc: %v", err)
	}
}

// wholeLog is every entry the log holds, read through a handle that belongs to
// no machine in the test: a reader's own origin is the one origin it cannot
// read, so a third name reads all of them.
func wholeLog(t *testing.T, log *synclog.MemTransport) []synclog.Entry {
	t.Helper()
	entries, err := log.OnLog("log-reader").Pull(nil)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	return entries
}

// TestAPeerDoesNotHandASecretBack pins the echo rule at the pipeline level. A
// value that arrived from a peer is not this machine's edit, so it does not
// stamp, so the next export does not put the peer's own value on the log again
// with a newer stamp.
func TestAPeerDoesNotHandASecretBack(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)
	b.attempt(t)

	if _, ok := b.writeStamp(t, "secrets/"+config.SecretTypesafe); ok {
		t.Error("the applying machine stamped an applied secret")
	}
	b.attempt(t)
	for _, e := range wholeLog(t, log) {
		if e.Origin == b.mdb.Origin() && e.Table == "doc:"+config.Secrets {
			t.Errorf("B re-exported the value it applied: %s", e.Body)
		}
	}
}

// TestTheR2CredentialsAPersonTypedReachAPeer pins the enable's half of the
// stamping. A machine that was handed the R2 flags has been given them by a
// person, so they travel: the point of the enable storing them is that a second
// machine can then sync bodies without the owner re-running it there.
//
// The joiner's own SetR2 is not what this exercises -- that machine already holds
// them -- so the peer starts with none at all.
func TestTheR2CredentialsAPersonTypedReachAPeer(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	for _, name := range []string{
		relevosync.SecretR2Endpoint, relevosync.SecretR2Bucket,
		relevosync.SecretR2KeyID, relevosync.SecretR2Secret,
	} {
		if got := b.secret(t, name); got != "" {
			t.Fatalf("B starts holding %s; the case proves nothing", name)
		}
	}

	if err := relevosync.SetR2(a.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2 on A: %v", err)
	}
	a.attempt(t)
	b.attempt(t)

	got, err := relevosync.ReadR2(b.local)
	if err != nil {
		t.Fatalf("ReadR2 on B: %v", err)
	}
	if !got.Complete() {
		t.Errorf("B's R2 credentials are incomplete after the pull: missing %v", got.Missing())
	}
	if got != verbFixtureR2 {
		t.Errorf("B's R2 credentials = %+v, want A's", got)
	}
}

// TestAJoinerReceivesTheR2Credentials pins the path a new machine depends on: it
// joins with nothing but the code and the passphrase, and the credentials it had
// no way of being told arrive in the document.
func TestAJoinerReceivesTheR2Credentials(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, false)

	if err := relevosync.SetR2(a.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2 on A: %v", err)
	}
	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)

	b.join(t)
	b.attempt(t)

	for _, name := range []string{
		relevosync.SecretR2Endpoint, relevosync.SecretR2Bucket,
		relevosync.SecretR2KeyID, relevosync.SecretR2Secret,
	} {
		if b.secret(t, name) == "" {
			t.Errorf("B holds no %s after the join", name)
		}
	}
	if got := b.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Errorf("B's typesafe = %q, want A's value", got)
	}
}

// TestAnUnstampedClientKeyNeverCrossesTheLog pins the identity rule at the only
// place it matters. Every machine generated its own key before this document
// existed, so an unstamped one crossing the log would hand one machine's
// install the fleet's identity and un-enrol every machine already holding one.
func TestAnUnstampedClientKeyNeverCrossesTheLog(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	if err := a.cfg.PutSecretUnstamped(config.SecretClientKey, pem); err != nil {
		t.Fatalf("PutSecretUnstamped: %v", err)
	}
	// An unstamped write signals nothing, so the export would never ask for the
	// document and the rule under test would never run. The signal is raised by
	// hand here because the rule is what Items answers when it *is* asked: a
	// machine that signals the document for any other reason must still not hand
	// out the key, and a test that never asks proves nothing.
	a.signalSecrets(t)
	a.attempt(t)
	b.attempt(t)

	if got := b.secret(t, config.SecretClientKey); got != "" {
		t.Error("a generated client key reached the peer")
	}
	for _, e := range wholeLog(t, log) {
		if e.Origin == a.mdb.Origin() && strings.Contains(string(e.Body), config.SecretClientKey) {
			t.Errorf("the log carries a client key entry: %s", e.Body)
		}
	}
}

// TestAStampedClientKeyReachesThePeer is the other half: once a person has
// chosen, the key travels, and it travels sealed.
func TestAStampedClientKeyReachesThePeer(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	a.setSecret(t, config.SecretClientKey, string(pem))
	a.attempt(t)
	b.attempt(t)

	got := b.secret(t, config.SecretClientKey)
	if got == "" {
		t.Fatal("a client key a person set did not reach the peer")
	}
	if _, err := remote.ParsePrivate([]byte(got)); err != nil {
		t.Errorf("the arrived client key does not parse: %v", err)
	}
	for _, e := range wholeLog(t, log) {
		if strings.Contains(string(e.Body), string(pem)) {
			t.Errorf("the log carries the client key in the clear: %s", e.Body)
		}
	}
}

// TestASecretRemovalTravels pins the tombstone path through the pipeline. A
// removal that did not cross the log would be handed straight back by the next
// export from the machine that still held the value.
func TestASecretRemovalTravels(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)
	b.attempt(t)
	if b.secret(t, config.SecretTypesafe) == "" {
		t.Fatal("B never held the value to lose")
	}

	if err := a.cfg.SecretDelete(config.SecretTypesafe); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}
	a.attempt(t)
	b.attempt(t)

	if got := b.secret(t, config.SecretTypesafe); got != "" {
		t.Errorf("B still holds the removed secret: %q", got)
	}
}

// TestALockedPeerParksTheSecrets pins the lock half: a machine that cannot
// verify an entry must not apply it, and must apply it once it is unlocked --
// the same path every other document takes, which is what binding it in
// sharedDocs buys.
func TestALockedPeerParksTheSecrets(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, nil, true)

	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.attempt(t)
	b.attempt(t)

	if got := b.secret(t, config.SecretTypesafe); got != "" {
		t.Fatalf("the locked machine applied the secret %q, want it parked", got)
	}
	parked, err := b.mdb.PendingDocs()
	if err != nil {
		t.Fatalf("PendingDocs: %v", err)
	}
	found := false
	for _, p := range parked {
		if strings.Contains(p.Entry, config.Secrets) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no parked entry names the secrets document: %d parked in all", len(parked))
	}

	if err := relevosync.SetKey(b.local, cfgSyncKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if _, err := relevosync.DrainParkedDocs(b.mdb, b.local, b.log.OnLog(b.mdb.Origin())); err != nil {
		t.Fatalf("DrainParkedDocs: %v", err)
	}
	if got := b.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Errorf("after the unlock B's typesafe = %q, want the parked value", got)
	}
}

// forgeSecretEntry appends one signed `doc:secrets` entry to the log as if a
// peer had sent it: a real MAC under the fleet key, so the entry passes the
// signature check the way an honest one does, and the importer's document is
// what has to refuse it.
//
// The exporter never builds such an entry -- it filters the allow-list and seals
// with the reader's own key -- so these are the two cases the real pipeline
// cannot produce on its own: an entry sealed under another key, and an entry
// naming a secret that does not travel.
func forgeSecretEntry(t *testing.T, log *synclog.MemTransport, from *cfgMachine, name string, value []byte) {
	t.Helper()
	forgeSecretEntries(t, log, from, map[string]json.RawMessage{name: json.RawMessage(value)})
}

// forgeSecretEntries appends one signed `doc:secrets` entry per name, all in one
// batch, as if a peer had sent them: real MACs under the fleet key, so the
// entries pass the signature check the way honest ones do and the importer's
// document is what has to refuse them.
//
// The exporter never builds such entries -- it filters the allow-list and seals
// with the reader's own key -- so these are the two cases the real pipeline
// cannot produce on its own: a value sealed under another key, and a value
// naming a secret that does not travel.
func forgeSecretEntries(t *testing.T, log *synclog.MemTransport, from *cfgMachine, values map[string]json.RawMessage) {
	t.Helper()
	var entries []synclog.Entry
	for _, name := range sortedSecretNames(values) {
		entry, err := synclog.NewDocUpsert(forgedOrigin, from.cfg.SecretsDoc(), name, 1,
			forgedStamp, values[name], time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("build the doc entry: %v", err)
		}
		entry.MAC = synclog.EntryMAC(cfgSyncKey, entry)
		entries = append(entries, entry)
	}
	boundary := synclog.Boundary{Seq: 1, Tag: synclog.BoundaryTag(cfgSyncKey, forgedOrigin, 1)}
	if _, err := log.OnLog(forgedOrigin).AppendSigned(entries, boundary); err != nil {
		t.Fatalf("append the doc entries: %v", err)
	}
}

// sortedSecretNames is the batch's key order, so a forged batch is the same
// every run rather than a map walk.
func sortedSecretNames(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// The forged entries come from one installation that signs nothing else, so its
// first signed sequence is the one the boundary names and every forged entry
// lands in a batch of its own. The stamp is far past anything a real machine
// wrote, so none of them is a redelivery of anything applied.
const forgedOrigin = "m9"

var forgedStamp synclog.Stamp = 1 << 40

// TestAnUnreadableSecretIsDroppedWithoutTheValue pins the import refusal at the
// pipeline level. A value sealed under another key authenticates under neither
// the reader's key nor the wrong name, so it is refused -- and the refusal this
// machine writes down, which is what a reader of its history would see, carries
// the name and the reason and never the value.
func TestAnUnreadableSecretIsDroppedWithoutTheValue(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	sealed, err := relevosync.SealSecret([]byte("ffffffffffffffffffffffffffffffff"),
		config.SecretTypesafe, []byte(canarySecret))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	forgeSecretEntry(t, log, a, config.SecretTypesafe, sealed)
	b.pull(t)

	if got := b.secret(t, config.SecretTypesafe); got != "" {
		t.Fatalf("B applied a value sealed under another key: %q", got)
	}
	for _, text := range b.revisionText(t) {
		if strings.Contains(text, canarySecret) {
			t.Fatalf("a revision carries the plaintext:\n%s", text)
		}
		if strings.Contains(text, string(sealed)) {
			t.Fatalf("a revision carries the envelope:\n%s", text)
		}
	}
	if !b.anySource(t, "sync-superseded:secrets:") {
		t.Errorf("B's revision sources %v lack the supersede record for the refused secret", b.sources(t))
	}
}

// TestAPeerCannotSendAMachineLocalSecret pins the import half of the allow-list.
// A peer that names the sync key or the token gets a refusal and no write: those
// are this machine's own, and a fleet that shared them would be one identity.
func TestAPeerCannotSendAMachineLocalSecret(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	if err := relevosync.SetToken(a.local, []byte("a-token-for-the-case"), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken on A: %v", err)
	}
	if err := relevosync.SetToken(b.local, []byte("b-token-for-the-case"), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken on B: %v", err)
	}
	bKey := b.secret(t, relevosync.SecretKey)

	// A well-formed body under each off-list name, so the refusal has to come
	// from the document's own name check rather than from an envelope this
	// build cannot read.
	offList := map[string]json.RawMessage{}
	for _, name := range []string{relevosync.SecretKey, relevosync.SecretToken, "serve.tls.key", "unknown.name"} {
		if _, err := relevosync.SealSecret(cfgSyncKey, name, []byte("a-value-a-peer-tried-to-set")); !errors.Is(err, relevosync.ErrSecretEnvelope) {
			t.Errorf("SealSecret(%s) = %v, want the off-list refusal", name, err)
		}
		offList[name] = json.RawMessage(`{"v":1,"n":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","c":"AAAA"}`)
	}
	forgeSecretEntries(t, log, a, offList)
	b.pull(t)

	if !b.anySource(t, "sync-superseded:secrets:") {
		t.Errorf("B's revision sources %v lack the refusal record for the off-list names", b.sources(t))
	}
	if got := b.secret(t, relevosync.SecretToken); got != "b-token-for-the-case" {
		t.Errorf("B's token = %q, want the one B held", got)
	}
	if got := b.secret(t, relevosync.SecretKey); got != bKey {
		t.Errorf("B's sync key = %q, want the one B held", got)
	}
	if bKey == "" {
		t.Error("B holds no sync key, so the case proves nothing about a real machine")
	}
}

// revisionText is every revision row as one string per row, which is the whole
// history a reader of this machine could open.
func (m *cfgMachine) revisionText(t *testing.T) []string {
	t.Helper()
	rows, err := m.cfg.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, string(r.Changes)+string(r.Message)+string(r.Source)+string(r.Snapshot))
	}
	return out
}

func (m *cfgMachine) anySource(t *testing.T, prefix string) bool {
	t.Helper()
	for _, s := range m.sources(t) {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
