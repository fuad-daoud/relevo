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

// The two secret names a caller outside the R2 set owns. They are spelled here
// rather than imported because the config package imports this one, so the name
// that decides whether a value travels cannot itself be decided by the package
// that reads the value.
const (
	SecretClientKey = "client.key"
	SecretTypesafe  = "typesafe"
)

// SecretsDocName is the name the secrets document travels under, as
// `doc:secrets` on the log. It lives here rather than in the config package that
// owns the document, because the two packages that write the document -- config
// for a person's edit and a peer's value, this one for the R2 flags an enable
// carries -- would otherwise each spell the name and the stamp key separately,
// and the two spellings would drift until a stamped write became invisible to
// the exporter reading the other.
const SecretsDocName = "secrets"

// SecretStampKey is the write-stamp key of one shared secret. The prefix is what
// keeps a secret's stamp from being read as a config section's stamp: both live
// in one table, and no config section is named after a document.
func SecretStampKey(name string) string { return SecretsDocName + "/" + name }

// StampSecretWrites records that this machine wrote each named secret as a
// person's edit: one write stamp per name, and one signal for the document, all
// in the caller's transaction so a stamp is never spent on a secret the machine
// did not keep.
//
// The stamps come from the hybrid clock rather than the wall clock, for the same
// reason an entry's stamp does: a machine whose clock is behind a peer's must
// still win its own later edit against that peer's earlier one.
//
// A caller with a secret value the program generated passes none of this. It is
// the whole difference between a machine choosing a fleet's client key and one
// generating its own.
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

// ErrNoKey marks a caller that needs the sync key on a machine that holds none:
// a locked machine, or one that never synced. It is what the secrets document
// answers with rather than an empty document, because an exporter reads an empty
// document as a machine that holds no secrets at all and would send a removal
// for every one the fleet has.
var ErrNoKey = errors.New("sync: this machine holds no sync key")

// ErrSecretEnvelope marks a value this build cannot read as a sealed secret:
// a name off the list, an envelope over the cap, a version this build does not
// know, or a field this version does not define. A refusal carries this so a
// caller can tell a value it must not read from a store it could not reach.
var ErrSecretEnvelope = errors.New("sync: not a sealed secret this build reads")

// The envelope versions this build reads. Version 1 is the shape every machine
// before the first rekey wrote and reads: no epoch field, sealed under the one
// key there is. Version 2 is the same shape with the epoch it was sealed under
// named, and it is what every machine from epoch 2 on seals with -- so an
// upgrade alone writes nothing new, and only a rekey changes the bytes.
const (
	secretEnvelopeVersion  = 1
	secretEnvelopeVersion2 = 2
)

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

// secretNonceLabel is the HKDF info that separates the key the sealing nonce is
// hashed under from the AEAD key itself: reusing the encryption key as the HMAC
// key would make the nonce a second output of the same derivation, and distinct
// uses of one key need labels, not operator discipline.
const secretNonceLabel = "relevo-sync-secret-nonce-v1"

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
// than being read as that other secret. The epoch decides the envelope: one
// seals v1 exactly as every machine before the first rekey did, and anything
// above one seals v2 naming its epoch.
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

// sealNonce derives the sealing nonce from what the envelope is of: HMAC-SHA256
// under a key derived from the secrets subkey with its own label, over a
// length-prefixed encoding of the name and the value, truncated to a nonce.
// The prefix keeps two inputs from agreeing as one: without it a name's
// boundary is invisible, and a caller under the same key could hand a value to
// a name so the pair hashed to another pair's nonce.
//
// The trade: equal (name, value) pairs under one key seal to the same
// envelope, which is what lets the exporter's digest see an unchanged value and
// keeps an echo of the fleet's own values off the log; what that reveals to a
// log reader is only that a value equals one it held before.
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

// sealNonceKey derives the key the sealing nonce is hashed under: HKDF-SHA256
// over the secrets subkey with the nonce's own label, so the derivation the
// envelope is encrypted under and the derivation the nonce is hashed under are
// never the same bytes.
func sealNonceKey(subkey []byte, name string) ([]byte, error) {
	key := make([]byte, secretSubkeyLen)
	r := hkdf.New(sha256.New, subkey, nil, []byte(secretNonceLabel))
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("secrets %s: derive the nonce key: %w: %w", name, err, db.ErrInvalid)
	}
	return key, nil
}

// SealSecret seals one allow-listed secret value under the newest epoch's key
// of the keyring given, and returns the envelope as the document's JSON value.
// An epoch-1 ring seals v1, byte-identical to what every earlier build wrote;
// anything newer seals v2 naming its epoch. A value that does not fit the cap
// is refused rather than sent, on the same rule as an unreadable ledger: the
// caller keeps its signal and reports, and a peer never has to read a shape
// this build would have to truncate to accept.
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

// sealingKey is the epoch and the key a seal runs under: the newest the ring
// holds, validated so a ring that names no key it holds never seals at all.
func (r Keyring) sealingKey() (int, []byte, error) {
	key, err := r.newest()
	if err != nil {
		return 0, nil, err
	}
	return r.Epoch, key, nil
}

// OpenSecret reads one sealed secret back under its own name. Version 1 opens
// under the epoch-1 key -- the only key the shape names -- and version 2 under
// the key of the epoch its `e` names, which the ring must hold: an envelope
// sealed under an epoch this machine does not hold is refused, never opened
// under a key that happens to be nearest. Every refusal here happens before a
// single plaintext byte is produced, and none of them formats the ciphertext:
// a refusal is read by whoever holds the log.
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

// secretEnvelope is the shape a sealed secret travels in. The version is a
// field of the envelope rather than of the entry, so a reader can refuse it
// before it opens anything and a later writer cannot reach a reader that has no
// rule for what it wrote. Epoch is the epoch version 2 was sealed under; a
// version-1 envelope names no epoch because it names the only one there was.
type secretEnvelope struct {
	V int    `json:"v"`
	E int    `json:"e,omitempty"`
	N string `json:"n"`
	C string `json:"c"`
}

// Epoch is the epoch the envelope was sealed under: what its `e` names on
// version 2, and one on version 1, which is the epoch the shape was the whole
// of.
func (e secretEnvelope) Epoch() int {
	if e.V == secretEnvelopeVersion2 && e.E > 0 {
		return e.E
	}
	return 1
}

// readSecretEnvelope decodes an envelope and refuses every shape this build has
// no reader for. Each refusal is its own sentence because each answers a
// different peer: an unknown field is a writer that added something this build
// would ignore, an unknown version is a writer whose key rules cannot be applied
// here at all, and a value over the cap is not a secret this document carries.
// A version-2 envelope that names no epoch is a shape nothing sealed, so it is
// refused here rather than read as epoch 1 -- the epoch is the version's whole
// point.
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
		// The epoch field is version 2's and version 2's only. A version-1
		// envelope carrying one is a writer this build would otherwise ignore
		// the field of, and ignoring it would read the value on terms its
		// writer did not choose.
		return env, envelopeRefusal(name, `carries field "e" on version 1`)
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
