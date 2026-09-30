package config

import (
	"bytes"
	"encoding/pem"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// mustMarshal returns kp's private key PEM, the current-labelled bytes.
func mustMarshal(t *testing.T, kp remote.Keypair) []byte {
	t.Helper()
	pemBytes, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	return pemBytes
}

// legacyPEM re-labels kp's PEM as the pre-rename type, the shape a client key
// stored before the rename has on disk.
func legacyPEM(t *testing.T, kp remote.Keypair) []byte {
	t.Helper()
	block, _ := pem.Decode(mustMarshal(t, kp))
	if block == nil {
		t.Fatal("MarshalPrivate produced no PEM block")
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RELAY ED25519 PRIVATE KEY", // name-guard: legacy
		Bytes: block.Bytes,
	})
}

// seedRawSecret stores value under name without a Store write, simulating a
// secret written before PutSecret canonicalised the client key's label.
func seedRawSecret(t *testing.T, s *Store, name string, value []byte) {
	t.Helper()
	if err := s.db.Tx(func(t *db.Tx) error {
		return t.SecretPut(name, value, s.now().UTC())
	}); err != nil {
		t.Fatalf("SecretPut(%s): %v", name, err)
	}
}

// TestNormalizeClientKeyConvergesLegacyLabel pins the write half: a legacy
// label is rewritten once, under the current label and the same key, and the
// write is recorded as a daemon revision.
func TestNormalizeClientKeyConvergesLegacyLabel(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	seedRawSecret(t, s, SecretClientKey, legacyPEM(t, kp))

	wrote, err := s.NormalizeClientKey()
	if err != nil {
		t.Fatalf("NormalizeClientKey: %v", err)
	}
	if !wrote {
		t.Fatal("NormalizeClientKey wrote = false, want true for a legacy label")
	}

	got, ok, err := s.Secret(SecretClientKey)
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if !ok {
		t.Fatal("client.key is missing after NormalizeClientKey")
	}
	want := mustMarshal(t, kp)
	if !bytes.Equal(got, want) {
		t.Fatalf("stored key = %q, want the current-labelled bytes %q", got, want)
	}
	parsed, err := remote.ParsePrivate(got)
	if err != nil {
		t.Fatalf("ParsePrivate(stored): %v", err)
	}
	if gotID, wantID := remote.IDOf(parsed.Public), remote.IDOf(kp.Public); gotID != wantID {
		t.Fatalf("id after normalize = %q, want %q", gotID, wantID)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Log = %d rows, want 1", len(rows))
	}
	if rows[0].Source != "daemon" {
		t.Errorf("source = %q, want daemon", rows[0].Source)
	}
	var ops []string
	for _, c := range revChanges(t, rows[0]) {
		if c.Path == "secret."+SecretClientKey {
			ops = append(ops, c.Op)
		}
	}
	if len(ops) != 1 || ops[0] != "set" {
		t.Errorf("changes = %v, want one secret.%s set", ops, SecretClientKey)
	}
}

// TestNormalizeClientKeyLeavesCurrentLabelAlone pins the byte-identical no-op:
// a current-labelled row is untouched and records no revision.
func TestNormalizeClientKeyLeavesCurrentLabelAlone(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := s.As("test", "seed client key").PutSecret(SecretClientKey, mustMarshal(t, kp)); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	before, _, err := s.Secret(SecretClientKey)
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}

	wrote, err := s.NormalizeClientKey()
	if err != nil {
		t.Fatalf("NormalizeClientKey: %v", err)
	}
	if wrote {
		t.Error("NormalizeClientKey wrote = true, want false for a current label")
	}

	after, _, err := s.Secret(SecretClientKey)
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("stored key changed from %q to %q", before, after)
	}
	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("Log = %d rows, want only the seed revision", len(rows))
	}
}

// TestNormalizeClientKeyWithoutAKeyIsANoop pins the absent-key branch: nothing
// stored, nothing written, no revision.
func TestNormalizeClientKeyWithoutAKeyIsANoop(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	wrote, err := s.NormalizeClientKey()
	if err != nil {
		t.Fatalf("NormalizeClientKey: %v", err)
	}
	if wrote {
		t.Error("NormalizeClientKey wrote = true with no stored key")
	}
	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("Log = %d rows, want 0", len(rows))
	}
}

// TestPutSecretStoresCurrentLabelFromLegacy pins the canonicalising write: a
// legacy PEM handed to PutSecret is stored under the current label.
func TestPutSecretStoresCurrentLabelFromLegacy(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := s.As("test", "seed client key").PutSecret(SecretClientKey, legacyPEM(t, kp)); err != nil {
		t.Fatalf("PutSecret(legacy PEM): %v", err)
	}

	got, ok, err := s.Secret(SecretClientKey)
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if !ok {
		t.Fatal("client.key is missing after PutSecret")
	}
	if remote.IsLegacyPrivatePEM(got) {
		t.Fatalf("stored key still carries the legacy label: %q", got)
	}
	if want := mustMarshal(t, kp); !bytes.Equal(got, want) {
		t.Fatalf("stored key = %q, want %q", got, want)
	}
}
