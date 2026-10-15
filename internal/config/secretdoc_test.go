package config

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests drive the secrets document directly, against a real store and a
// real secret table: the document's own refusals are pure enough to pin without
// a pipeline, and the export side needs a store a person stamped through.

const secretDocKey = "0123456789abcdef0123456789abcdef"

// canarySecret is the plaintext no surface in these tests may print. Every
// assertion is about it staying unreadable, so it is a value nothing else in the
// tree holds.
var canarySecret = []byte("CANARY-3f9a-plaintext-that-must-never-travel")

// secretStore is a store holding the sync key, which is the one thing a store
// needs before the document will export anything.
func secretStore(t *testing.T) *Store {
	t.Helper()
	s := openStore(t)
	if err := sync.SetKey(s.db, []byte(secretDocKey), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	return s
}

func sealedFor(t *testing.T, name string, plain []byte) json.RawMessage {
	t.Helper()
	env, err := sync.SealSecret([]byte(secretDocKey), name, plain)
	if err != nil {
		t.Fatalf("SealSecret(%s): %v", name, err)
	}
	return env
}

// held reports what the store holds under a name, and fails when it holds
// something: most of these tests are about a value that must not be there.
func held(t *testing.T, s *Store, name string) []byte {
	t.Helper()
	value, ok, err := s.Secret(name)
	if err != nil {
		t.Fatalf("Secret(%s): %v", name, err)
	}
	if !ok {
		return nil
	}
	return value
}

func TestSecretDocNameAndStampKey(t *testing.T) {
	t.Parallel()
	if Secrets != "secrets" {
		t.Errorf("Secrets = %q, want %q", Secrets, "secrets")
	}
	if got := secretStampKey(SecretTypesafe); got != "secrets/typesafe" {
		t.Errorf("secretStampKey = %q, want %q", got, "secrets/typesafe")
	}
	if secretStampKey(SecretTypesafe) == SecretTypesafe {
		t.Error("a secret's stamp key is the bare name, which a config section could claim")
	}
}

// TestItemsRefusesOnALockedMachine is the rule the exporter depends on: a locked
// machine answers with an error, not with an empty document, because the
// exporter reads an absent key as one this machine no longer holds and would
// send a removal for every secret the fleet has.
func TestItemsRefusesOnALockedMachine(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if err := sync.DeleteKey(s.db); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}

	items, err := s.SecretsDoc().Items()
	if !errors.Is(err, sync.ErrNoKey) {
		t.Fatalf("Items on a locked machine = %v, %v; want the no-key refusal", items, err)
	}
	if items != nil {
		t.Fatalf("Items returned %d items alongside its refusal; an empty document would tombstone the fleet",
			len(items))
	}
}

// TestLockedItemsKeepsTheExportSignal pins the other half: the refusal leaves
// the signal standing, so the value is exported once the machine is unlocked
// rather than silently dropped.
func TestLockedItemsKeepsTheExportSignal(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if err := sync.DeleteKey(s.db); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}

	signals, err := s.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if !hasSignal(signals, Secrets) {
		t.Fatalf("signals = %v; a locked export dropped the document's signal", signals)
	}

	if err := sync.SetKey(s.db, []byte(secretDocKey), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	items, err := s.SecretsDoc().Items()
	if err != nil {
		t.Fatalf("Items after the unlock: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Items after the unlock = %d items, want the one secret", len(items))
	}
}

func hasSignal(signals []db.DocSignal, doc string) bool {
	for _, s := range signals {
		if s.Doc == doc {
			return true
		}
	}
	return false
}

// TestItemsExportsTheAllowListAndNothingElse pins both halves of the list at the
// export: the six shared names travel, and every other name in the table --
// which is most of it -- does not. A walk by name rather than by list would hand
// a peer this machine's bearer token and its sync key.
func TestItemsExportsTheAllowListAndNothingElse(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	shared := map[string][]byte{
		SecretTypesafe:        canarySecret,
		sync.SecretR2Endpoint: []byte("https://acct.r2.example"),
		sync.SecretR2Bucket:   []byte("bkt"),
		sync.SecretR2KeyID:    []byte("kid"),
		sync.SecretR2Secret:   []byte("r2-secret"),
	}
	for name, value := range shared {
		if err := s.PutSecret(name, value); err != nil {
			t.Fatalf("PutSecret(%s): %v", name, err)
		}
	}
	for _, name := range []string{sync.SecretKey, sync.SecretToken, "serve.tls.key", "agy.chat-1", "unknown.name"} {
		if err := s.db.Tx(func(t *db.Tx) error { return t.SecretPut(name, canarySecret, time.Unix(0, 0).UTC()) }); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	items, err := s.SecretsDoc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	for name := range shared {
		if _, ok := items[name]; !ok {
			t.Errorf("%s is missing from the exported document", name)
		}
	}
	for _, name := range []string{sync.SecretKey, sync.SecretToken, "serve.tls.key", "agy.chat-1", "unknown.name", SecretClientKey} {
		if _, ok := items[name]; ok {
			t.Errorf("%s is in the exported document, want it kept here", name)
		}
	}
	if len(items) != len(shared) {
		t.Errorf("exported %d items, want the %d allow-listed secrets", len(items), len(shared))
	}
	for name, item := range items {
		if strings.Contains(string(item), string(canarySecret)) {
			t.Errorf("the exported %s carries the plaintext: %s", name, item)
		}
	}
}

// TestAnUnstampedClientKeyIsNeverExported pins the rule a fleet's identity rests
// on. Every machine generated its own client key before this document existed,
// so exporting an unstamped one would hand a joiner's fresh install the fleet's
// identity and silently un-enrol every machine already holding one.
func TestAnUnstampedClientKeyIsNeverExported(t *testing.T) {
	t.Parallel()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}

	t.Run("generated", func(t *testing.T) {
		s := secretStore(t)
		if err := s.PutSecretUnstamped(SecretClientKey, pem); err != nil {
			t.Fatalf("PutSecretUnstamped: %v", err)
		}
		items, err := s.SecretsDoc().Items()
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		if _, ok := items[SecretClientKey]; ok {
			t.Error("a generated client key was exported; no person chose it")
		}
	})

	t.Run("normalised", func(t *testing.T) {
		s := secretStore(t)
		// Seeded straight into the table with the pre-rename label, which is
		// what a key written before the canonicalising write existed holds.
		legacy := legacyPEM(t, kp)
		if err := s.db.Tx(func(tx *db.Tx) error {
			return tx.SecretPut(SecretClientKey, legacy, time.Unix(0, 0).UTC())
		}); err != nil {
			t.Fatalf("seed the legacy key: %v", err)
		}
		if wrote, err := s.NormalizeClientKey(); err != nil || !wrote {
			t.Fatalf("NormalizeClientKey = %v, %v; want a write", wrote, err)
		}
		items, err := s.SecretsDoc().Items()
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		if _, ok := items[SecretClientKey]; ok {
			t.Error("a re-marshalled client key was exported; the re-marshal is the program's own write")
		}
	})

	t.Run("stamped by a person", func(t *testing.T) {
		s := secretStore(t)
		if err := s.PutSecret(SecretClientKey, pem); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
		items, err := s.SecretsDoc().Items()
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		item, ok := items[SecretClientKey]
		if !ok {
			t.Fatal("a client key a person set was not exported")
		}
		if strings.Contains(string(item), string(pem)) {
			t.Error("the exported client key carries the plaintext PEM")
		}
	})
}

// TestPreExistingAllowListedSecretsTravelUnstamped pins the other half: only the
// client key is conditional. The R2 credentials and typesafe a laptop already
// held before this document existed must travel on the first unlocked export, or
// a joiner never gets them and the owner has to re-run enable by hand.
func TestPreExistingAllowListedSecretsTravelUnstamped(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	// Written before the store held a key, by the pre-5a path, which left no
	// write stamp behind.
	for name, value := range map[string]string{
		SecretTypesafe:        "ts-pre-existing",
		sync.SecretR2Endpoint: "https://acct.r2.example",
		sync.SecretR2Bucket:   "bkt",
		sync.SecretR2KeyID:    "kid",
		sync.SecretR2Secret:   "r2-secret",
	} {
		if err := s.db.Tx(func(t *db.Tx) error {
			return t.SecretPut(name, []byte(value), time.Unix(0, 0).UTC())
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := sync.SetKey(s.db, []byte(secretDocKey), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}

	items, err := s.SecretsDoc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("exported %d items, want the five pre-existing allow-listed secrets: %v", len(items), sortedKeysOf(items))
	}
	for name := range items {
		if _, ok, err := s.SecretsDoc().ItemWriteStamp(name); err != nil {
			t.Fatalf("ItemWriteStamp(%s): %v", name, err)
		} else if ok {
			t.Errorf("%s carries a write stamp; the pre-existing write recorded none", name)
		}
	}
}

func sortedKeysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestApplyRefusesAnOffListName pins the import-side half of the list. A peer
// that sends a name this document does not carry gets a refusal and nothing
// else: not the value, not a tombstone, not a revision.
func TestApplyRefusesAnOffListName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{sync.SecretKey, sync.SecretToken, "serve.tls.key", "unknown.name", ""} {
		s := secretStore(t)
		if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
		before := map[string][]byte{}
		for _, n := range []string{name, sync.SecretKey} {
			before[n] = held(t, s, n)
		}
		doc := s.SecretsDoc()
		refusal, err := doc.applyChecked(t, name, sealedFor(t, SecretTypesafe, canarySecret))
		// The document's own refusal, not the envelope's: the value is a
		// perfectly good sealed secret, sealed for an allowed name, and what
		// stops it is the name it arrived under.
		if !errors.Is(refusal, ErrNotSharedSecret) {
			t.Errorf("ApplyIn(%q) = %v, %v; want the off-list refusal", name, refusal, err)
		}
		if err != nil {
			t.Errorf("ApplyIn(%q) failed rather than refusing: %v", name, err)
		}
		for _, n := range []string{name, sync.SecretKey} {
			if got := held(t, s, n); string(got) != string(before[n]) {
				t.Errorf("ApplyIn(%q) changed %q to %q", name, n, got)
			}
		}
	}
}

// TestApplyRefusesEveryUnreadableEnvelope pins that the import side refuses
// before it writes, so an envelope this build cannot read never leaves the
// secret table half-changed.
func TestApplyRefusesEveryUnreadableEnvelope(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"an unknown version": `{"v":2,"n":"aa","c":"bb"}`,
		"an unknown field":   `{"v":1,"n":"aa","c":"bb","e":3}`,
		"not an object":      `"sealed"`,
		"empty":              ``,
	}
	for name, raw := range cases {
		s := secretStore(t)
		if err := s.PutSecret(SecretTypesafe, []byte("what-this-machine-held")); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
		refusal, err := s.SecretsDoc().applyChecked(t, SecretTypesafe, json.RawMessage(raw))
		if !errors.Is(refusal, sync.ErrSecretEnvelope) {
			t.Errorf("an envelope with %s = %v, %v; want the envelope refusal", name, refusal, err)
		}
		if err != nil {
			t.Errorf("an envelope with %s failed rather than refusing: %v", name, err)
		}
		if got := held(t, s, SecretTypesafe); string(got) != "what-this-machine-held" {
			t.Errorf("an envelope with %s changed the stored value to %q", name, got)
		}
	}
}

// TestApplyRefusesAnUnreadableClientKey pins the one value whose landing would
// break something outside this machine: an arriving client key that does not
// parse would replace the identity every server this machine talks to knows.
func TestApplyRefusesAnUnreadableClientKey(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	// A well-sealed value that is not a private key: it passes the envelope
	// checks and fails the one that matters.
	env := sealedFor(t, SecretClientKey, canarySecret)
	refusal, err := s.SecretsDoc().applyChecked(t, SecretClientKey, env)
	if !errors.Is(refusal, ErrInvalidValue) {
		t.Fatalf("an unsealed client key = %v, %v; want the private-key refusal", refusal, err)
	}
	if got := held(t, s, SecretClientKey); got != nil {
		t.Fatalf("the refused client key was written: %q", got)
	}
}

// TestApplyWritesAnAllowedSecret is the path that has to work: a sealed value
// this machine holds the key for lands in the secret table.
func TestApplyWritesAnAllowedSecret(t *testing.T) {
	t.Parallel()
	for _, name := range []string{SecretTypesafe, sync.SecretR2Bucket, sync.SecretR2Secret} {
		s := secretStore(t)
		refusal, err := s.SecretsDoc().applyChecked(t, name, sealedFor(t, name, canarySecret))
		if refusal != nil || err != nil {
			t.Fatalf("ApplyIn(%s) = %v, %v; want the value to land", name, refusal, err)
		}
		if got := held(t, s, name); string(got) != string(canarySecret) {
			t.Errorf("%s = %q after the apply, want the sealed value", name, got)
		}
	}
}

// TestApplyTombstoneClearsTheSecret pins that a removal lands as a removal. The
// document carries it as an upsert whose value is null, so the stamp reaches the
// reader that clears the name rather than being lost with an absent body.
func TestApplyTombstoneClearsTheSecret(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	refusal, err := s.SecretsDoc().applyChecked(t, SecretTypesafe, nil)
	if refusal != nil || err != nil {
		t.Fatalf("the tombstone = %v, %v; want it to land", refusal, err)
	}
	if got := held(t, s, SecretTypesafe); got != nil {
		t.Fatalf("the secret is still held after a tombstone: %q", got)
	}
}

// TestAnAppliedSecretIsNotStamped pins the echo rule. A value a peer sent is
// recorded as arriving and does not stamp, because a stamp says this machine
// wrote the key -- and a stamped arrival would be re-exported as though this
// machine had decided it, which is how one machine's copy of a credential
// becomes the fleet's by accident.
func TestAnAppliedSecretIsNotStamped(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	if refusal, err := s.SecretsDoc().applyChecked(t, SecretTypesafe, sealedFor(t, SecretTypesafe, canarySecret)); refusal != nil || err != nil {
		t.Fatalf("ApplyIn = %v, %v", refusal, err)
	}
	if _, ok, err := s.SecretsDoc().ItemWriteStamp(SecretTypesafe); err != nil {
		t.Fatalf("ItemWriteStamp: %v", err)
	} else if ok {
		t.Fatal("an applied secret carries a write stamp")
	}
	signals, err := s.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if hasSignal(signals, Secrets) {
		t.Error("an applied secret signalled the document, so the peer would receive it straight back")
	}
	if got := held(t, s, SecretTypesafe); string(got) != string(canarySecret) {
		t.Errorf("the applied value = %q, want the sealed one", got)
	}
}

// TestAPersonsEditStampsAndSignals pins the other half: the write that is a
// person's stamps its key and signals the document, so it travels.
func TestAPersonsEditStampsAndSignals(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	stamp, ok, err := s.SecretsDoc().ItemWriteStamp(SecretTypesafe)
	if err != nil || !ok {
		t.Fatalf("a person's edit left no write stamp: ok=%v err=%v", ok, err)
	}
	if stamp <= 0 {
		t.Errorf("the write stamp is %d, want a position in the clock", stamp)
	}
	signals, err := s.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if !hasSignal(signals, Secrets) {
		t.Errorf("signals = %v; a person's edit of a shared secret was not signalled", signals)
	}
}

// TestAPersonsRemovalStampsAndSignals pins the same for a removal. A removal
// that did not travel would be handed straight back by the next export from the
// machine that still held the value.
func TestAPersonsRemovalStampsAndSignals(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if err := s.SecretDelete(SecretTypesafe); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}
	if _, ok, err := s.SecretsDoc().ItemWriteStamp(SecretTypesafe); err != nil {
		t.Fatalf("ItemWriteStamp: %v", err)
	} else if !ok {
		t.Error("a person's removal left no write stamp, so the removal would not travel")
	}
	if got := held(t, s, SecretTypesafe); got != nil {
		t.Errorf("the secret is still held after the removal: %q", got)
	}
}

// TestAnImportedSecretFileStamps pins the third person's path. A config import
// is unstamped for every section it writes, so the secret files inside it need
// their own entry point or an imported client key would never travel.
func TestAnImportedSecretFileStamps(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	dir := t.TempDir()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	writeFile(t, filepath.Join(dir, "client.key"), string(pem), 0o600)
	writeFile(t, filepath.Join(dir, "typesafe.key"), "ts-from-import", 0o600)

	if _, err := s.ImportFiles(dir, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}
	if err := sync.SetKey(s.db, []byte(secretDocKey), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	items, err := s.SecretsDoc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if _, ok := items[SecretClientKey]; !ok {
		t.Error("an imported client key was not exported; a file a person dropped in is their edit")
	}
	if _, ok := items[SecretTypesafe]; !ok {
		t.Error("an imported typesafe key was not exported")
	}
	// The sections the import wrote stay unstamped, which is the rule the
	// import's blanket flag exists for.
	if _, ok, err := s.db.ConfigWriteStamp(string(Candidates)); err != nil {
		t.Fatalf("ConfigWriteStamp: %v", err)
	} else if ok {
		t.Error("the import stamped a config section; only its secret files are a person's edit")
	}
}

// TestSupersedeInRecordsTheKeyAndTheReasonOnly pins the redaction rule at the
// one place a losing value could have been written down. A revision is read back
// by anyone who can open this machine's history, so it carries the name and the
// reason and nothing else.
func TestSupersedeInRecordsTheKeyAndTheReasonOnly(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	env := sealedFor(t, SecretTypesafe, canarySecret)
	if err := s.db.Tx(func(t *db.Tx) error {
		return s.SecretsDoc().SupersedeIn(t, SecretTypesafe, env, "peer-1",
			"an entry stamped 5 from peer-1 is older than the one applied, stamped 9 from peer-2")
	}); err != nil {
		t.Fatalf("SupersedeIn: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("revisions = %d, want the one the supersede recorded", len(rows))
	}
	row := rows[0]
	text := string(row.Changes) + string(row.Message) + string(row.Source) + string(row.Snapshot)
	if strings.Contains(text, string(canarySecret)) {
		t.Fatalf("the superseding revision carries the plaintext:\n%s", text)
	}
	if strings.Contains(text, strings.Trim(string(env), `"{}`)) {
		t.Fatalf("the superseding revision carries the envelope:\n%s", text)
	}
	if !strings.Contains(string(row.Changes), "secret."+SecretTypesafe) {
		t.Errorf("the revision does not name the secret: %s", row.Changes)
	}
	if !strings.Contains(string(row.Message), "older than") {
		t.Errorf("the revision does not carry the reason: %q", row.Message)
	}
	if !strings.Contains(row.Source, "peer-1") {
		t.Errorf("the revision does not name the installation that sent it: %q", row.Source)
	}
}

// TestNoRevisionEverCarriesASecretValue pins the same rule on the write paths,
// because a revision is the one place a value could reach a reader by accident:
// the stamped write, the unstamped write, the removal and the arrival all record
// through one code path and all of them must record the name only.
func TestNoRevisionEverCarriesASecretValue(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if err := s.PutSecretUnstamped(sync.SecretR2Secret, canarySecret); err != nil {
		t.Fatalf("PutSecretUnstamped: %v", err)
	}
	if refusal, err := s.SecretsDoc().applyChecked(t, sync.SecretR2Bucket, sealedFor(t, sync.SecretR2Bucket, canarySecret)); refusal != nil || err != nil {
		t.Fatalf("ApplyIn: %v, %v", refusal, err)
	}
	if err := s.SecretDelete(SecretTypesafe); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}
	if err := s.PutSecret(SecretClientKey, pem); err != nil {
		t.Fatalf("PutSecret(client.key): %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no revision was recorded at all")
	}
	for _, row := range rows {
		text := string(row.Changes) + string(row.Message) + string(row.Source) + string(row.Snapshot)
		if strings.Contains(text, string(canarySecret)) {
			t.Fatalf("revision %d carries the plaintext:\n%s", row.Rev, text)
		}
		if strings.Contains(text, strings.TrimSuffix(strings.TrimPrefix(string(pem), "-----BEGIN "), "\n")) {
			t.Fatalf("revision %d carries the client key:\n%s", row.Rev, text)
		}
	}
}

// TestSecretNamesStillListNamesOnly pins the surface a person reads: the list
// says which secrets this machine holds and never what they are.
func TestSecretNamesStillListNamesOnly(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	names, err := s.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames: %v", err)
	}
	if len(names) != 2 { // typesafe and the sync key
		t.Fatalf("names = %v, want typesafe and the sync key", names)
	}
	for _, name := range names {
		if strings.Contains(name, string(canarySecret)) {
			t.Fatalf("a secret name carries its value: %s", name)
		}
	}
}

// TestTheStampSurvivesAnUnstampedWrite pins that an unstamped write clears
// neither the stamp nor the signal. The signal a previous stamped write left is
// still about a value this machine holds, so a normalisation that follows a
// person's edit must not swallow the edit's claim to travel.
func TestTheStampSurvivesAnUnstampedWrite(t *testing.T) {
	t.Parallel()
	s := secretStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if err := s.PutSecretUnstamped(SecretTypesafe, []byte("ts-re-marshalled")); err != nil {
		t.Fatalf("PutSecretUnstamped: %v", err)
	}
	if _, ok, err := s.SecretsDoc().ItemWriteStamp(SecretTypesafe); err != nil || !ok {
		t.Fatalf("the unstamped write cleared the stamp: ok=%v err=%v", ok, err)
	}
	signals, err := s.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if !hasSignal(signals, Secrets) {
		t.Errorf("signals = %v; the unstamped write cleared the document's signal", signals)
	}
}

// applyChecked drives the document's refusal half through the real transaction
// the importer uses, so a test sees the same two answers the importer does.
func (d *SecretDoc) applyChecked(t *testing.T, key string, value json.RawMessage) (error, error) {
	t.Helper()
	var refusal, err error
	txErr := d.store.db.Tx(func(tx *db.Tx) error {
		refusal, err = d.ApplyIn(tx, key, value, "peer-1")
		return nil
	})
	if txErr != nil {
		t.Fatalf("the transaction refused to open: %v", txErr)
	}
	return refusal, err
}

// the document answers for a store and a clock, both of which the pipeline reads
// through the synclog interfaces.
var (
	_ synclog.SharedDoc = (*SecretDoc)(nil)
	_ synclog.TxDoc     = (*SecretDoc)(nil)
)
