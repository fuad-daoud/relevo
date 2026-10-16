package config

// The secrets document under key epochs: what it seals at the newest epoch,
// what it opens of the history an older epoch wrote, and the refusal that keeps
// a machine from reading bytes its keyring cannot name.

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/sync"
)

// epochStore is a store whose machine holds the whole keyring of a rekeyed
// fleet: the epoch-1 key, the epoch-2 key that replaced it, and the epoch row
// naming two.
func epochStore(t *testing.T) (*Store, []byte) {
	t.Helper()
	s := openStore(t)
	key1 := []byte("0123456789abcdef0123456789abcdef")
	key2 := []byte("fedcba9876543210fedcba9876543210")
	ring := sync.Keyring{Epoch: 2, Keys: map[int][]byte{1: key1, 2: key2}}
	if err := sync.SetKeys(s.db, ring, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKeys: %v", err)
	}
	return s, key2
}

// TestItemsSealUnderTheNewestEpoch pins what a rekeyed machine sends: version-2
// envelopes naming the epoch they were sealed under, and plaintexts that open
// only under that epoch's key.
func TestItemsSealUnderTheNewestEpoch(t *testing.T) {
	t.Parallel()
	s, key2 := epochStore(t)
	if err := s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	items, err := s.SecretsDoc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	var shape struct {
		V int `json:"v"`
		E int `json:"e"`
	}
	if err := json.Unmarshal(items[SecretTypesafe], &shape); err != nil {
		t.Fatalf("decode the sealed item: %v", err)
	}
	if shape.V != 2 || shape.E != 2 {
		t.Fatalf("the item sealed as v%d e%d, want v2 e2", shape.V, shape.E)
	}
	ring := sync.Keyring{Epoch: 2, Keys: map[int][]byte{2: key2}}
	plain, err := sync.OpenSecret(ring, SecretTypesafe, items[SecretTypesafe])
	if err != nil || string(plain) != string(canarySecret) {
		t.Fatalf("the item did not open under the epoch-2 key: %q, %v", plain, err)
	}
}

// TestTheDocOpensHistoryAtEveryEpochItHolds pins the reading half: a rekeyed
// machine still applies the version-1 envelopes exported before the rekey, and
// a machine that has not learned of the rekey refuses version-2 bytes rather
// than reading them under a rule they were not sealed under.
func TestTheDocOpensHistoryAtEveryEpochItHolds(t *testing.T) {
	t.Parallel()
	s, _ := epochStore(t)
	key1 := []byte("0123456789abcdef0123456789abcdef")
	old, err := sync.SealSecret(sync.Keyring{Epoch: 1, Keys: map[int][]byte{1: key1}},
		SecretTypesafe, []byte("the value before the rekey"))
	if err != nil {
		t.Fatalf("seal the old envelope: %v", err)
	}
	if err := s.SecretsDoc().Apply(SecretTypesafe, old); err != nil {
		t.Fatalf("Apply a v1 envelope on a rekeyed machine: %v", err)
	}
	if got := held(t, s, SecretTypesafe); string(got) != "the value before the rekey" {
		t.Fatalf("held = %q, want the old plaintext", got)
	}

	// The 5a-direction refusal: a machine that holds only the epoch-1 key is
	// handed a version-2 envelope and refuses it -- the value it already holds
	// stands, and nothing is written behind the refusal.
	fresh := secretStore(t)
	key2 := []byte("fedcba9876543210fedcba9876543210")
	newest, err := sync.SealSecret(sync.Keyring{Epoch: 2, Keys: map[int][]byte{2: key2}},
		SecretTypesafe, []byte("the value after the rekey"))
	if err != nil {
		t.Fatalf("seal the new envelope: %v", err)
	}
	if err := fresh.SecretsDoc().Apply(SecretTypesafe, newest); !errors.Is(err, sync.ErrSecretEnvelope) {
		t.Fatalf("Apply a v2 envelope on an epoch-1 machine = %v, want a refusal", err)
	}
	if _, ok, err := fresh.Secret(SecretTypesafe); err != nil || ok {
		t.Fatalf("the refused envelope wrote the secret: (ok %v, err %v)", ok, err)
	}
}

// TestALockedApplyErrorsRatherThanEmptying pins the locked machine's check: an
// arriving value on a machine with no key is refused with the no-key error, so
// the value is parked for the unlock rather than dropped.
func TestALockedApplyErrorsRatherThanEmptying(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	env, err := sync.SealSecret(sync.Keyring{Epoch: 1, Keys: map[int][]byte{1: []byte(secretDocKey)}},
		SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if err := sync.DeleteKey(s.db); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if err := s.SecretsDoc().Apply(SecretTypesafe, env); !errors.Is(err, sync.ErrNoKey) {
		t.Fatalf("Apply on a locked machine = %v, want the no-key refusal", err)
	}
	if _, ok, err := s.Secret(SecretTypesafe); err != nil || ok {
		t.Fatalf("the refused apply wrote the secret: (ok %v, err %v)", ok, err)
	}
}
