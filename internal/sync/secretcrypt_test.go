package sync

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// canarySecret is the plaintext these tests refuse to lose. Every assertion is
// about it not being readable, so it is a value nothing else in the tree holds.
var canarySecret = []byte("CANARY-3f9a-plaintext-that-must-never-travel")

func testSyncKey() []byte { return []byte("0123456789abcdef0123456789abcdef") }

// TestSecretSubkeyIsNotTheSyncKey pins the separation the label exists for: the
// key a secret is sealed under is not the key entries are MACed with, and not
// any slice of it, so a signature key cannot open a sealed value.
func TestSecretSubkeyIsNotTheSyncKey(t *testing.T) {
	t.Parallel()
	key := testSyncKey()
	sub, err := SecretSubkey(key)
	if err != nil {
		t.Fatalf("SecretSubkey: %v", err)
	}
	if len(sub) != secretSubkeyLen {
		t.Fatalf("subkey is %d bytes, want %d", len(sub), secretSubkeyLen)
	}
	if bytes.Equal(sub, key) {
		t.Fatal("the secret subkey is the sync key itself")
	}
	if bytes.HasPrefix(key, sub) || bytes.HasPrefix(sub, key) {
		t.Fatalf("the subkey %x is a slice of the sync key %x; they are not independent", sub, key)
	}
	// A key readable from the sync key with nothing but the public label would
	// not separate the two, so the derivation has to actually consume the key.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(secretKeyLabel))
	if bytes.Equal(mac.Sum(nil), sub) {
		t.Fatal("the subkey is the sync key MACed with the public label, which needs no work factor")
	}
}

func TestSecretSubkeyIsDerivedAndLabelBound(t *testing.T) {
	t.Parallel()
	first, err := SecretSubkey(testSyncKey())
	if err != nil {
		t.Fatalf("SecretSubkey: %v", err)
	}
	again, err := SecretSubkey(testSyncKey())
	if err != nil {
		t.Fatalf("SecretSubkey again: %v", err)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("the same sync key derived two different subkeys")
	}
	other, err := SecretSubkey([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatalf("SecretSubkey on another key: %v", err)
	}
	if bytes.Equal(first, other) {
		t.Fatal("two sync keys derived the same subkey")
	}
	if _, err := SecretSubkey(nil); !errors.Is(err, db.ErrInvalid) {
		t.Fatalf("SecretSubkey(nil) = %v, want a refusal: a locked machine must not seal under a key of zeros", err)
	}
}

func TestSealAndOpenRoundTripEveryAllowedSecret(t *testing.T) {
	t.Parallel()
	for _, name := range AllowedSecrets() {
		env, err := SealSecret(testSyncKey(), name, canarySecret)
		if err != nil {
			t.Fatalf("SealSecret %s: %v", name, err)
		}
		out, err := OpenSecret(testSyncKey(), name, env)
		if err != nil {
			t.Fatalf("OpenSecret %s: %v", name, err)
		}
		if !bytes.Equal(out, canarySecret) {
			t.Fatalf("%s: opened %q, want the sealed value", name, out)
		}
		if bytes.Contains(env, canarySecret) {
			t.Fatalf("%s: the envelope carries the plaintext: %s", name, env)
		}
	}
}

// TestSealDrawsANoncePerValue pins that sealing is not a fixed function of its
// input: two envelopes of one value differ, so the log cannot tell that two
// machines sealed the same secret, and neither envelope can be replayed as the
// other.
func TestSealDrawsANoncePerValue(t *testing.T) {
	t.Parallel()
	first, err := SealSecret(testSyncKey(), SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	second, err := SealSecret(testSyncKey(), SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret again: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("sealing the same value twice produced one envelope")
	}
	for _, env := range []json.RawMessage{first, second} {
		if out, err := OpenSecret(testSyncKey(), SecretTypesafe, env); err != nil || !bytes.Equal(out, canarySecret) {
			t.Fatalf("a fresh envelope did not open back to the value: %q %v", out, err)
		}
	}
}

// TestSealedValueIsBoundToItsOwnName pins the associated data: a ciphertext
// sealed for one secret cannot be read as another, so a peer that moves a value
// to a name of its choosing gets an authentication failure rather than a
// secret under a name nobody chose.
func TestSealedValueIsBoundToItsOwnName(t *testing.T) {
	t.Parallel()
	env, err := SealSecret(testSyncKey(), SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if _, err := OpenSecret(testSyncKey(), SecretClientKey, env); !errors.Is(err, ErrSecretEnvelope) {
		t.Fatalf("a value sealed for %s opened under %s: %v", SecretTypesafe, SecretClientKey, err)
	}
	if _, err := OpenSecret(testSyncKey(), SecretR2Secret, env); !errors.Is(err, ErrSecretEnvelope) {
		t.Fatalf("a value sealed for %s opened under %s: %v", SecretTypesafe, SecretR2Secret, err)
	}
}

func TestOpenRefusesAnotherMachinesKey(t *testing.T) {
	t.Parallel()
	env, err := SealSecret(testSyncKey(), SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	other := []byte("ffffffffffffffffffffffffffffffff")
	if _, err := OpenSecret(other, SecretTypesafe, env); !errors.Is(err, ErrSecretEnvelope) {
		t.Fatalf("a value sealed under one key opened under another: %v", err)
	}
	if _, err := OpenSecret(nil, SecretTypesafe, env); err == nil {
		t.Fatal("a locked machine opened a sealed secret")
	}
}

// TestAllowSecretListsExactlyTheSharedNames pins the allow-list. Every name that
// travels is named here, and the names a machine must never hand a peer are not:
// the fleet's R2 credentials cross, its identity and its bearer do not.
func TestAllowSecretListsExactlyTheSharedNames(t *testing.T) {
	t.Parallel()
	want := []string{
		SecretClientKey, SecretTypesafe,
		SecretR2Endpoint, SecretR2Bucket, SecretR2KeyID, SecretR2Secret,
	}
	sort.Strings(want)
	got := AllowedSecrets()
	if !slices.Equal(got, want) {
		t.Fatalf("AllowedSecrets() = %v, want %v", got, want)
	}
	for _, name := range []string{SecretKey, SecretToken, "turso.token", "serve.tls.cert", "agy.chat-1", ""} {
		if AllowSecret(name) {
			t.Errorf("%q is allow-listed; only the six shared names travel", name)
		}
	}
	for _, name := range want {
		if !AllowSecret(name) {
			t.Errorf("%q is not allow-listed", name)
		}
	}
}

// TestOffListNamesAreRefusedOnBothSides pins that the list is checked when a
// value is sealed and when one is opened, so a name that does not travel cannot
// be produced and cannot be consumed.
func TestOffListNamesAreRefusedOnBothSides(t *testing.T) {
	t.Parallel()
	for _, name := range []string{SecretKey, SecretToken, "serve.tls.key", "anything.unknown", ""} {
		if _, err := SealSecret(testSyncKey(), name, canarySecret); !errors.Is(err, ErrSecretEnvelope) {
			t.Errorf("SealSecret(%q) = %v, want a refusal", name, err)
		}
		if _, err := OpenSecret(testSyncKey(), name, []byte(`{"v":1,"n":"","c":""}`)); !errors.Is(err, ErrSecretEnvelope) {
			t.Errorf("OpenSecret(%q) = %v, want a refusal", name, err)
		}
	}
}

// TestOpenRefusesAnUnknownEnvelopeVersion pins the version rule the next step
// of rekey depends on: a writer that adds an epoch writes a different version,
// and this build refuses it rather than reading the fields it recognises under
// a rule it does not have.
func TestOpenRefusesAnUnknownEnvelopeVersion(t *testing.T) {
	t.Parallel()
	sealed, err := SealSecret(testSyncKey(), SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(sealed, &env); err != nil {
		t.Fatalf("read the sealed envelope: %v", err)
	}
	for _, version := range []string{"0", "2", "3", `-1`} {
		env["v"] = json.RawMessage(version)
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("re-encode with v=%s: %v", version, err)
		}
		out, err := OpenSecret(testSyncKey(), SecretTypesafe, raw)
		if !errors.Is(err, ErrSecretEnvelope) {
			t.Fatalf("an envelope at version %s opened (%q): want a refusal", version, out)
		}
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("the refusal for v=%s does not name the version: %v", version, err)
		}
	}
}

// TestOpenRefusesAnUnknownEnvelopeField pins the second half of the same rule: a
// version this build does read is still refused when it carries a field this
// version does not define, because dropping the field would apply the value on
// terms its writer did not choose.
func TestOpenRefusesAnUnknownEnvelopeField(t *testing.T) {
	t.Parallel()
	sealed, err := SealSecret(testSyncKey(), SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(sealed, &env); err != nil {
		t.Fatalf("read the sealed envelope: %v", err)
	}
	for _, field := range []string{"e", "k", "epoch", "key_id"} {
		epochEnvelope(t, sealed, field, "7")
	}
	// A fresh envelope, because the refusal names one field out of however
	// many are present and the read walks the fields in map order.
	epochEnvelope(t, sealed, "e", "1")
}

// epochEnvelope is the envelope a rekey writer would produce: this build's
// shape with one extra field carrying a key epoch. It is the case the version
// rule has to fail closed on, so it gets a name of its own.
func epochEnvelope(t *testing.T, sealed json.RawMessage, field, value string) json.RawMessage {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal(sealed, &env); err != nil {
		t.Fatalf("read the sealed envelope: %v", err)
	}
	env[field] = json.RawMessage(value)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("re-encode with %q: %v", field, err)
	}
	if _, err := OpenSecret(testSyncKey(), SecretTypesafe, raw); !errors.Is(err, ErrSecretEnvelope) ||
		!strings.Contains(err.Error(), fmt.Sprintf("%q", field)) {
		t.Fatalf("the refusal for a key epoch does not name the field: %v", err)
	}
	return raw
}

func TestOpenRefusesEveryUnreadableEnvelopeShape(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"no envelope":       ``,
		"not an object":     `"sealed"`,
		"an array":          `[]`,
		"null":              `null`,
		"no version":        `{"n":"aa","c":"bb"}`,
		"no nonce":          `{"v":1,"c":"bb"}`,
		"no ciphertext":     `{"v":1,"n":"aa"}`,
		"a short nonce":     `{"v":1,"n":"aaaa","c":"aaaa"}`,
		"a short cipher":    `{"v":1,"n":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","c":"AAAA"}`,
		"a padded nonce":    `{"v":1,"n":"AQEBAQEBAQEBAQEBAQEBAQEBAQE=","c":"AAAA"}`,
		"a version as text": `{"v":"1","n":"aa","c":"bb"}`,
	} {
		if _, err := OpenSecret(testSyncKey(), SecretTypesafe, json.RawMessage(raw)); !errors.Is(err, ErrSecretEnvelope) {
			t.Errorf("an envelope with %s opened: want a refusal", name)
		}
	}
}

// TestSealRefusesAValueOverTheCap pins the cap on the way out: an over-cap value
// is an error the caller keeps its signal over, never a truncated secret that
// would authenticate and read back wrong.
func TestSealRefusesAValueOverTheCap(t *testing.T) {
	t.Parallel()
	// The cap is measured on the sealed envelope, so the largest value that
	// fits is found by sealing rather than by arithmetic: the encoding grows
	// faster than the plaintext on some lengths and slower on others.
	var (
		fits []byte
		env  json.RawMessage
		err  error
	)
	for n := SecretEnvelopeMax; n > 0; n-- {
		candidate := bytes.Repeat([]byte("x"), n)
		sealed, serr := SealSecret(testSyncKey(), SecretTypesafe, candidate)
		if serr == nil {
			fits, env, err = candidate, sealed, nil
			break
		}
	}
	if err != nil {
		t.Fatalf("no value under the cap sealed: %v", err)
	}
	if len(env) > SecretEnvelopeMax {
		t.Fatalf("the envelope is %d bytes, over the cap", len(env))
	}
	if len(fits) >= SecretEnvelopeMax {
		t.Fatalf("a %d byte plaintext sealed into a %d byte envelope; the cap is not binding",
			len(fits), len(env))
	}
	over := append(bytes.Repeat([]byte("x"), len(fits)+1), []byte("y")...)
	if _, err := SealSecret(testSyncKey(), SecretTypesafe, over); !errors.Is(err, ErrSecretEnvelope) {
		t.Fatalf("an over-cap value sealed: %v", err)
	} else if !strings.Contains(err.Error(), "cap") {
		t.Errorf("the over-cap refusal does not name the cap: %v", err)
	}
}

func TestOpenRefusesAnOverCapEnvelope(t *testing.T) {
	t.Parallel()
	raw := append([]byte(`{"v":1,"n":"aaaa","c":"`), bytes.Repeat([]byte("b"), SecretEnvelopeMax)...)
	raw = append(raw, []byte(`"}`)...)
	if _, err := OpenSecret(testSyncKey(), SecretTypesafe, raw); !errors.Is(err, ErrSecretEnvelope) {
		t.Fatalf("an over-cap envelope opened: %v", err)
	} else if !strings.Contains(err.Error(), "cap") {
		t.Errorf("the over-cap refusal does not name the cap: %v", err)
	}
}

// TestRefusalsNeverCarryTheValue pins the redaction rule at its source: a
// refusal names the secret and the rule, and neither the plaintext nor the
// ciphertext appears in it, because every refusal ends up in a log line.
func TestRefusalsNeverCarryTheValue(t *testing.T) {
	t.Parallel()
	sealed, err := SealSecret(testSyncKey(), SecretTypesafe, canarySecret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	refusals := []error{
		func() error { _, err := OpenSecret(testSyncKey(), SecretR2Secret, sealed); return err }(),
		func() error {
			_, err := OpenSecret([]byte("ffffffffffffffffffffffffffffffff"), SecretTypesafe, sealed)
			return err
		}(),
		func() error {
			_, err := OpenSecret(testSyncKey(), SecretTypesafe, []byte(`{"v":9,"n":"aa","c":"bb"}`))
			return err
		}(),
		func() error {
			_, err := OpenSecret(testSyncKey(), SecretTypesafe, append(sealed, ' ', 'z'))
			return err
		}(),
	}
	for n, err := range refusals {
		if err == nil {
			t.Fatalf("refusal %d did not refuse", n)
		}
		text := err.Error()
		if strings.Contains(text, string(canarySecret)) {
			t.Fatalf("refusal %d carries the plaintext: %v", n, err)
		}
		if strings.Contains(text, strings.Trim(string(sealed), `"{}`)) {
			t.Fatalf("refusal %d carries the envelope: %v", n, err)
		}
		if !strings.Contains(text, "secrets") {
			t.Errorf("refusal %d does not name the document: %v", n, err)
		}
	}
}
