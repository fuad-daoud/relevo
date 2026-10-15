package sync

// What a secret looks like on its way between machines: the derived key one
// value is sealed under, the sealed shape, and the list of the names that may
// travel at all. Everything here is pure -- no database, no clock, no network --
// so the rule a reader refuses an entry by is a rule a test pins without a
// machine behind it.
//
// The seal is not a transport detail. A shared document's value rides the log
// as an ordinary JSON body inside an entry any machine can read, so a secret
// value is ciphertext with the fleet's key or it is nothing: the plaintext is
// sealed before an export is built and opened after an entry has been checked
// against the key, never in between.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The two secret names a caller outside the R2 set owns. They are spelled here
// rather than imported because the config package imports this one, so the name
// that decides whether a value travels cannot itself be decided by the package
// that reads the value.
const (
	SecretClientKey = "client.key"
	SecretTypesafe  = "typesafe"
)

// ErrSecretEnvelope marks a value this build cannot read as a sealed secret:
// a name off the list, an envelope over the cap, a version this build does not
// know, or a field this version does not define. A refusal carries this so a
// caller can tell a value it must not read from a store it could not reach.
var ErrSecretEnvelope = errors.New("sync: not a sealed secret this build reads")

// secretEnvelopeVersion is the only envelope this build reads. A later step adds
// a key epoch to the envelope itself rather than a field to this shape, so a
// peer on that step fails closed here instead of decrypting under an epoch this
// machine does not have.
const secretEnvelopeVersion = 1

// SecretEnvelopeMax is the largest sealed secret that crosses the log, measured
// in bytes of the encoded envelope. The largest value a legitimate secret
// carries is a client key PEM of a few hundred bytes, so the cap sits an order
// above any real value and far below the point where one entry could be a
// payload rather than a secret.
//
// The document stays inline under this cap, never a reference to a body in the
// object store: a machine that has just joined holds no object-store
// credentials until this very document has been applied, so a secret that
// needed a fetch could not be the thing that grants the fetch.
const SecretEnvelopeMax = 8 << 10

// secretKeyLabel is the HKDF info that separates the secret-sealing key from
// the sync key every entry is MACed with, so the two are never the same bytes
// and a key that verifies a signature cannot decrypt anything.
const secretKeyLabel = "relevo-sync-secret-v1"

// secretSubkeyLen is the derived key's length: one XChaCha20-Poly1305 key.
const secretSubkeyLen = 32

// allowedSecrets is the whole list. Every other name in the secret table -- the
// token, the sync key, the serve certificates, anything a build does not know
// -- is this machine's own and stays here, which is what lets a peer be handed
// the fleet's R2 credentials without also being handed its identity.
var allowedSecrets = []string{
	SecretClientKey,
	SecretTypesafe,
	SecretR2Endpoint,
	SecretR2Bucket,
	SecretR2KeyID,
	SecretR2Secret,
}

// AllowSecret reports whether name travels. It is checked on the way out and on
// the way in, so a name that is not on the list is neither exported nor applied.
func AllowSecret(name string) bool { return slices.Contains(allowedSecrets, name) }

// AllowedSecrets returns the names that travel, sorted. It is what a refusal
// lists and what a note about the document reads from, so neither has to repeat
// the list and drift from it.
func AllowedSecrets() []string {
	out := append([]string(nil), allowedSecrets...)
	sort.Strings(out)
	return out
}

// SecretSubkey derives the key one secret value is sealed under, from the sync
// key. It is derived rather than reused so the bytes that authenticate an entry
// and the bytes that decrypt a secret are never the same, on either side of a
// compromise: reading a signature off the log must not read a secret with it.
//
// An empty key is a refusal rather than a key of zeros. A machine with no key is
// locked, and a locked machine must emit no secret at all -- including one
// sealed under a key every machine could derive.
func SecretSubkey(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("sync: %s is empty, so no secret can be sealed: %w", SecretKey, db.ErrInvalid)
	}
	sub := make([]byte, secretSubkeyLen)
	r := hkdf.New(sha256.New, key, nil, []byte(secretKeyLabel))
	if _, err := io.ReadFull(r, sub); err != nil {
		return nil, fmt.Errorf("sync: derive the secret key: %w: %w", err, db.ErrInvalid)
	}
	return sub, nil
}

// sealSecret seals value with the secret's own name as the associated data, so
// a ciphertext that arrives under another name fails to authenticate rather
// than being read as that other secret. One nonce is drawn per sealing, so the
// same value sealed twice is two different ciphertexts.
func sealSecret(subkey []byte, name string, value []byte) (json.RawMessage, error) {
	aead, err := chacha20poly1305.NewX(subkey)
	if err != nil {
		return nil, fmt.Errorf("secrets %s: open the cipher: %w: %w", name, err, db.ErrInvalid)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets %s: draw a nonce: %w: %w", name, err, db.ErrInvalid)
	}
	env, err := json.Marshal(secretEnvelope{
		V: secretEnvelopeVersion,
		N: base64.RawURLEncoding.EncodeToString(nonce),
		C: base64.RawURLEncoding.EncodeToString(aead.Seal(nil, nonce, value, []byte(name))),
	})
	if err != nil {
		return nil, fmt.Errorf("secrets %s: encode the envelope: %w: %w", name, err, db.ErrInvalid)
	}
	return env, nil
}

// SealSecret seals one allow-listed secret value under the sync key and returns
// the envelope as the document's JSON value. A value that does not fit the cap
// is refused rather than sent, on the same rule as an unreadable ledger: the
// caller keeps its signal and reports, and a peer never has to read a shape this
// build would have to truncate to accept.
func SealSecret(key []byte, name string, value []byte) (json.RawMessage, error) {
	if !AllowSecret(name) {
		return nil, secretNameRefusal(name)
	}
	sub, err := SecretSubkey(key)
	if err != nil {
		return nil, err
	}
	env, err := sealSecret(sub, name, value)
	if err != nil {
		return nil, err
	}
	if len(env) > SecretEnvelopeMax {
		return nil, fmt.Errorf("secrets %s: sealed value is %d bytes, over the %d byte cap: %w: %w",
			name, len(env), SecretEnvelopeMax, ErrSecretEnvelope, db.ErrInvalid)
	}
	return env, nil
}

// OpenSecret reads one sealed secret back under its own name. Every refusal
// here happens before a single plaintext byte is produced, and none of them
// formats the ciphertext: a refusal is read by whoever holds the log.
func OpenSecret(key []byte, name string, raw json.RawMessage) ([]byte, error) {
	if !AllowSecret(name) {
		return nil, secretNameRefusal(name)
	}
	env, err := readSecretEnvelope(name, raw)
	if err != nil {
		return nil, err
	}
	sub, err := SecretSubkey(key)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(sub)
	if err != nil {
		return nil, fmt.Errorf("secrets %s: open the cipher: %w: %w", name, err, db.ErrInvalid)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(env.N)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, envelopeRefusal(name, "carries no readable nonce")
	}
	body, err := base64.RawURLEncoding.DecodeString(env.C)
	if err != nil || len(body) < aead.Overhead() {
		return nil, envelopeRefusal(name, "carries no readable ciphertext")
	}
	out, err := aead.Open(nil, nonce, body, []byte(name))
	if err != nil {
		return nil, envelopeRefusal(name,
			"does not authenticate under this machine's key and this secret's name")
	}
	return out, nil
}

// secretEnvelope is the one shape a sealed secret travels in. The version is a
// field of the envelope rather than of the entry, so a reader can refuse it
// before it opens anything and a later writer cannot reach a reader that has no
// rule for what it wrote.
type secretEnvelope struct {
	V int    `json:"v"`
	N string `json:"n"`
	C string `json:"c"`
}

// readSecretEnvelope decodes an envelope and refuses every shape this build has
// no reader for. Each refusal is its own sentence because each answers a
// different peer: an unknown field is a writer that added something this build
// would ignore, an unknown version is a writer whose key rules cannot be applied
// here at all, and a value over the cap is not a secret this document carries.
func readSecretEnvelope(name string, raw json.RawMessage) (secretEnvelope, error) {
	var env secretEnvelope
	if len(raw) == 0 {
		return env, envelopeRefusal(name, "carries no envelope")
	}
	if len(raw) > SecretEnvelopeMax {
		return env, fmt.Errorf("secrets %s: envelope is %d bytes, over the %d byte cap: %w: %w",
			name, len(raw), SecretEnvelopeMax, ErrSecretEnvelope, db.ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return env, envelopeRefusal(name, "is not a JSON object")
	}
	for field := range fields {
		if field != "v" && field != "n" && field != "c" {
			return env, envelopeRefusal(name, fmt.Sprintf("carries unknown field %q", field))
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, envelopeRefusal(name, "does not decode")
	}
	if env.V != secretEnvelopeVersion {
		return env, fmt.Errorf("secrets %s: envelope version %d, this build reads only %d: %w",
			name, env.V, secretEnvelopeVersion, ErrSecretEnvelope)
	}
	return env, nil
}

// envelopeRefusal is one refusal of a sealed secret, carrying the name -- which
// is not a secret -- and never the value, the ciphertext or the nonce.
func envelopeRefusal(name, why string) error {
	return fmt.Errorf("secrets %s: envelope %s: %w", name, why, ErrSecretEnvelope)
}

// secretNameRefusal is the refusal of a name that does not travel. The name is
// named, because a peer that sent it has to be told which rule it broke and the
// name carries no value; a value that arrived under it is never printed.
func secretNameRefusal(name string) error {
	if name == "" {
		name = "(empty)"
	}
	return fmt.Errorf("secrets: %s is not a secret that travels: %w: %w", name, ErrSecretEnvelope, db.ErrInvalid)
}