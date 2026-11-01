package sync

// What a secret looks like between machines: the derived sealing key, the sealed
// shape, and the names that may travel. Everything here is pure -- no database,
// clock or network -- so a refusal rule is one a test pins without a machine.
// The seal is not a transport detail: a shared value rides the log as ordinary
// JSON any machine can read, so a secret is ciphertext with the fleet's key or
// nothing, sealed before an export is built and opened after an entry is checked.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The two secret names a caller outside the R2 set owns, spelled here rather
// than imported: the config package imports this one, so the travelling rule
// cannot be decided by the package reading the value.
const (
	SecretClientKey = "client.key"
	SecretTypesafe  = "typesafe"
)

// SecretsDocName is the name the secrets document travels under, as
// `doc:secrets` on the log. It lives here rather than in the config package that
// owns the document, because the two writers -- config for a person's edit, this
// package for an enable's R2 flags -- must agree on the name and stamp key or a
// stamped write turns invisible to the other reader.
const SecretsDocName = "secrets"

// SecretStampKey is the write-stamp key of one shared secret. The prefix keeps a
// secret's stamp from reading as a config section's: both live in one table.
func SecretStampKey(name string) string { return SecretsDocName + "/" + name }

// StampSecretWrites records that this machine wrote each named secret as a
// person's edit: one stamp per name and one signal for the document, all in the
// caller's transaction so a stamp is never spent on an unkept secret. Stamps come
// from the hybrid clock, so a machine behind a peer's clock still wins its later
// edit. A generated value passes none of this: choosing a fleet's client key
// differs from generating one's own.
func StampSecretWrites(t *db.Tx, names []string, now func() time.Time) error {
	if len(names) == 0 {
		return nil
	}
	for _, name := range names {
		stamp, err := synclog.NewHybridClockAt(t, now).Now()
		if err != nil {
			return err
		}
		if err := t.ConfigWriteStampPut(SecretStampKey(name), stamp.String()); err != nil {
			return err
		}
	}
	return t.SignalDoc(SecretsDocName)
}

// ErrNoKey marks a caller needing the sync key on a machine holding none. The
// secrets document answers with it rather than empty, because an exporter reads
// empty as holding no secrets and would send a removal for every one held.
var ErrNoKey = errors.New("sync: this machine holds no sync key")

// ErrSecretEnvelope marks a value this build cannot read as a sealed secret: a
// name off the list, an over-cap envelope, an unknown version or field. A refusal
// carries it so a caller tells an unreadable value from an unreachable store.
var ErrSecretEnvelope = errors.New("sync: not a sealed secret this build reads")

// The envelope versions this build reads. Version 1 names no epoch, sealed under
// the one key there is; version 2 names its epoch, sealed from epoch 2 on. An
// upgrade alone writes nothing new; only a rekey changes the bytes.
const (
	secretEnvelopeVersion  = 1
	secretEnvelopeVersion2 = 2
)

// SecretEnvelopeMax is the largest sealed secret crossing the log, in bytes of
// the encoded envelope. The largest legitimate value is a client key PEM of a
// few hundred bytes, so the cap sits an order above any real value and far below
// a payload. The document stays inline under this cap, never a body reference: a
// just-joined machine holds no object-store credentials until this very document
// is applied, so a secret needing a fetch could not grant the fetch.
const SecretEnvelopeMax = 8 << 10

// secretKeyLabel is the HKDF info separating the secret-sealing key from the sync
// key, so the two are never the same bytes and a verifying key cannot decrypt.
const secretKeyLabel = "relevo-sync-secret-v1"

// secretNonceLabel is the HKDF info separating the nonce-hashing key from the
// AEAD key itself, so distinct uses of one key take labels, not discipline.
const secretNonceLabel = "relevo-sync-secret-nonce-v1"

// secretSubkeyLen is the derived key's length: one XChaCha20-Poly1305 key.
const secretSubkeyLen = 32

// allowedSecrets is the whole list. Every other secret-table name is this
// machine's own and stays here, which lets a peer be handed the fleet's R2
// credentials without its identity.
var allowedSecrets = []string{
	SecretClientKey,
	SecretTypesafe,
	SecretR2Endpoint,
	SecretR2Bucket,
	SecretR2KeyID,
	SecretR2Secret,
}

// AllowSecret reports whether name travels, checked on the way out and the way in.
func AllowSecret(name string) bool { return slices.Contains(allowedSecrets, name) }

// AllowedSecrets returns the names that travel, sorted: what a refusal lists and
// a note reads from, so neither repeats the list and drifts.
func AllowedSecrets() []string {
	out := append([]string(nil), allowedSecrets...)
	sort.Strings(out)
	return out
}

// SecretSubkey derives the key one secret value is sealed under, from the sync
// key: derived, never reused, so authenticating bytes and decrypting bytes are
// never the same and a logged signature never reads a secret. An empty key is a
// refusal rather than zeros: a locked machine emits no secret at all.
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

// sealSecret seals value with the secret's own name as associated data, so a
// ciphertext under another name fails to authenticate. Epoch 1 seals v1; above
// one seals v2 naming its epoch.
func sealSecret(epoch int, subkey []byte, name string, value []byte) (json.RawMessage, error) {
	nonce, err := sealNonce(subkey, name, value)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(subkey)
	if err != nil {
		return nil, fmt.Errorf("secrets %s: open the cipher: %w: %w", name, err, db.ErrInvalid)
	}
	env := secretEnvelope{
		N: base64.RawURLEncoding.EncodeToString(nonce),
		C: base64.RawURLEncoding.EncodeToString(aead.Seal(nil, nonce, value, []byte(name))),
	}
	if epoch > 1 {
		env.V, env.E = secretEnvelopeVersion2, epoch
	} else {
		env.V = secretEnvelopeVersion
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("secrets %s: encode the envelope: %w: %w", name, err, db.ErrInvalid)
	}
	return raw, nil
}

// sealNonce derives the sealing nonce from the envelope's contents: HMAC-SHA256
// under the nonce's own derived key, over a length-prefixed name and value,
// truncated to a nonce. The prefix keeps a name boundary visible, so no pair
// hashes as another pair's nonce. Equal pairs seal to the same envelope, which
// lets the exporter digest an unchanged value and reveals only equality to a
// log reader.
func sealNonce(subkey []byte, name string, value []byte) ([]byte, error) {
	hkey, err := sealNonceKey(subkey, name)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, hkey)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(name)))
	mac.Write(size[:])
	mac.Write([]byte(name))
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	mac.Write(size[:])
	mac.Write(value)
	return mac.Sum(nil)[:chacha20poly1305.NonceSizeX], nil
}

// sealNonceKey derives the nonce-hashing key: HKDF-SHA256 over the secrets subkey
// with the nonce's own label, never the same bytes as the encryption derivation.
func sealNonceKey(subkey []byte, name string) ([]byte, error) {
	key := make([]byte, secretSubkeyLen)
	r := hkdf.New(sha256.New, subkey, nil, []byte(secretNonceLabel))
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("secrets %s: derive the nonce key: %w: %w", name, err, db.ErrInvalid)
	}
	return key, nil
}

// SealSecret seals one allow-listed secret value under the newest epoch's key.
// Epoch 1 seals v1; newer seals v2 naming its epoch. An over-cap value is refused
// rather than sent, so no peer reads a shape this build would truncate.
func SealSecret(ring Keyring, name string, value []byte) (json.RawMessage, error) {
	if !AllowSecret(name) {
		return nil, secretNameRefusal(name)
	}
	epoch, key, err := ring.sealingKey()
	if err != nil {
		return nil, err
	}
	sub, err := SecretSubkey(key)
	if err != nil {
		return nil, err
	}
	env, err := sealSecret(epoch, sub, name, value)
	if err != nil {
		return nil, err
	}
	if len(env) > SecretEnvelopeMax {
		return nil, fmt.Errorf("secrets %s: sealed value is %d bytes, over the %d byte cap: %w: %w",
			name, len(env), SecretEnvelopeMax, ErrSecretEnvelope, db.ErrInvalid)
	}
	return env, nil
}

// sealingKey is the epoch and key a seal runs under: the ring's newest, validated
// so a ring naming no held key never seals.
func (r Keyring) sealingKey() (int, []byte, error) {
	key, err := r.newest()
	if err != nil {
		return 0, nil, err
	}
	return r.Epoch, key, nil
}

// OpenSecret reads one sealed secret back under its own name: v1 under the epoch-1
// key, v2 under the key of its named epoch, which the ring must hold. Every
// refusal happens before a plaintext byte is produced, and none formats the
// ciphertext: a refusal is read by whoever holds the log.
func OpenSecret(ring Keyring, name string, raw json.RawMessage) ([]byte, error) {
	if !AllowSecret(name) {
		return nil, secretNameRefusal(name)
	}
	env, err := readSecretEnvelope(name, raw)
	if err != nil {
		return nil, err
	}
	epoch := env.Epoch()
	key := ring.Keys[epoch]
	if len(key) == 0 {
		return nil, fmt.Errorf("secrets %s: envelope names epoch %d, which this machine does not hold: %w",
			name, epoch, ErrSecretEnvelope)
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

// secretEnvelope is the shape a sealed secret travels in. The version is an
// envelope field, so a reader refuses it before opening anything. Epoch is what
// version 2 sealed under; version 1 names none, the only one there was.
type secretEnvelope struct {
	V int    `json:"v"`
	E int    `json:"e,omitempty"`
	N string `json:"n"`
	C string `json:"c"`
}

// Epoch is the epoch the envelope sealed under: its `e` on version 2, one on
// version 1.
func (e secretEnvelope) Epoch() int {
	if e.V == secretEnvelopeVersion2 && e.E > 0 {
		return e.E
	}
	return 1
}

// readSecretEnvelope decodes an envelope and refuses every shape this build has
// no reader for, each refusal answering a different peer: unknown field, unknown
// version, over-cap value, or a version-2 envelope naming no epoch -- which is the
// version's whole point, never read as epoch 1.
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
		if field != "v" && field != "n" && field != "c" && field != "e" {
			return env, envelopeRefusal(name, fmt.Sprintf("carries unknown field %q", field))
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, envelopeRefusal(name, "does not decode")
	}
	if env.V != secretEnvelopeVersion && env.V != secretEnvelopeVersion2 {
		return env, fmt.Errorf("secrets %s: envelope version %d, this build reads %d and %d: %w",
			name, env.V, secretEnvelopeVersion, secretEnvelopeVersion2, ErrSecretEnvelope)
	}
	if env.V == secretEnvelopeVersion2 && env.E < 2 {
		return env, envelopeRefusal(name, fmt.Sprintf("names epoch %d, which is not a rekeyed epoch", env.E))
	}
	if env.V == secretEnvelopeVersion && fields["e"] != nil {
		// The epoch field is version 2's only: ignoring it on version 1 would read the
		// value on terms its writer did not choose.
		return env, envelopeRefusal(name, `carries field "e" on version 1`)
	}
	return env, nil
}

// envelopeRefusal is one refusal of a sealed secret, carrying the name and never
// the value, ciphertext or nonce.
func envelopeRefusal(name, why string) error {
	return fmt.Errorf("secrets %s: envelope %s: %w", name, why, ErrSecretEnvelope)
}

// secretNameRefusal is the refusal of a name that does not travel, naming the
// name -- which carries no value -- so a peer learns its broken rule.
func secretNameRefusal(name string) error {
	if name == "" {
		name = "(empty)"
	}
	return fmt.Errorf("secrets: %s is not a secret that travels: %w: %w", name, ErrSecretEnvelope, db.ErrInvalid)
}
