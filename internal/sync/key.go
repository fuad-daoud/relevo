package sync

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/fuad-daoud/relevo/internal/db"
)

// SecretKey is the name the derived sync key is stored under, in the local secret
// table beside the token; the passphrase itself is never stored.
const SecretKey = "sync.key"

// KeyRemoteKDF is the local fact that the remote holds key material, 1 or 0: what
// lets status say locked without reaching the network.
const KeyRemoteKDF = "sync.remote_kdf"

// SyncRemoteEpoch is the local fact naming the newest epoch the remote holds, written
// by every attempt that could read it and left by one that could not.
const SyncRemoteEpoch = "sync.remote_epoch"

// SyncVouchTarget is the machine-local kv fact naming the sequence up to which this
// origin vouches for the tail appended before learning of a rekey.
const SyncVouchTarget = "sync.vouch_target"

// SyncKeyCheckMatch is the machine-local kv fact recording whether the held newest
// key's check matches the remote's at the same epoch: "1" or "0".
const SyncKeyCheckMatch = "sync.key_check_match"

// ErrEpochReplaced is what a machine whose held newest key check differs from the
// remote's at the same epoch reports: another machine's rekey overwrote this key.
// It stops signing and exporting and holds its import whole; unlock with the
// winner's passphrase to fix.
var ErrEpochReplaced = errors.New("sync: another machine's rekey replaced this one's key at the same epoch: run relevo db sync unlock with the passphrase that rekeyed the remote")

// SecretKeyEpoch is the secret-table row naming the newest held epoch, beside the
// key not in kv: a secret-table-only caller -- a document opening an arriving value
// -- reads the ring through that surface alone.
const SecretKeyEpoch = "sync.key.epoch"

// SecretKeyOldPrefix prefixes the secret-table row holding one older epoch's key.
const SecretKeyOldPrefix = "sync.key.old."

// ErrKDFParams marks a malformed or out-of-bounds KDF string; the remote chooses
// these numbers and is untrusted, so a huge memory cost refuses rather than allocates.
var ErrKDFParams = errors.New("sync: the remote's key derivation parameters are not acceptable")

const (
	kdfName       = "argon2id"
	kdfKeyLen     = 32
	keyCheckLabel = "relevo-sync-key-check-v1"

	kdfMinTime, kdfMaxTime       = 1, 4
	kdfMinMemory, kdfMaxMemory   = 19456, 262144
	kdfMinThreads, kdfMaxThreads = 1, 8
)

// KDFParams are the argon2id cost parameters, MemoryKiB in kibibytes.
type KDFParams struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

// DefaultKDF is what a new key derives with.
var DefaultKDF = KDFParams{Time: 3, MemoryKiB: 65536, Threads: 4}

// String formats the parameters as the remote stores them: the canonical shape
// ParseKDF also enforces.
func (p KDFParams) String() string {
	return fmt.Sprintf("%s;t=%d;m=%d;p=%d", kdfName, p.Time, p.MemoryKiB, p.Threads)
}

// ParseKDF parses a stored KDF string, refusing anything outside the bounds: the
// remote is untrusted, so a huge cost refuses rather than allocates.
func ParseKDF(s string) (KDFParams, error) {
	var t, m, p int
	parts := strings.Split(s, ";")
	if len(parts) != 4 || parts[0] != kdfName {
		return KDFParams{}, fmt.Errorf("%w: not of the form %s", ErrKDFParams, DefaultKDF)
	}
	for i, f := range []struct {
		prefix string
		out    *int
	}{{"t=", &t}, {"m=", &m}, {"p=", &p}} {
		v, err := strconv.Atoi(strings.TrimPrefix(parts[i+1], f.prefix))
		if err != nil || !strings.HasPrefix(parts[i+1], f.prefix) {
			return KDFParams{}, fmt.Errorf("%w: not of the form %s", ErrKDFParams, DefaultKDF)
		}
		*f.out = v
	}
	inBounds := t >= kdfMinTime && t <= kdfMaxTime &&
		m >= kdfMinMemory && m <= kdfMaxMemory &&
		p >= kdfMinThreads && p <= kdfMaxThreads
	if !inBounds {
		return KDFParams{}, fmt.Errorf("%w: outside the accepted bounds", ErrKDFParams)
	}
	params := KDFParams{Time: uint32(t), MemoryKiB: uint32(m), Threads: uint8(p)}
	if params.String() != s {
		return KDFParams{}, fmt.Errorf("%w: not in canonical form", ErrKDFParams)
	}
	return params, nil
}

// DeriveKey stretches the passphrase into the 32-byte sync key under the given costs.
func DeriveKey(passphrase, salt []byte, p KDFParams) []byte {
	return argon2.IDKey(passphrase, salt, p.Time, p.MemoryKiB, p.Threads, kdfKeyLen)
}

// KeyCheck is the value the remote holds distinguishing wrong from right
// passphrases; its label differs from the entry label, never doubling as an entry MAC.
func KeyCheck(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(keyCheckLabel))
	return mac.Sum(nil)
}

// SetKey stores the derived key as an epoch-1 ring, never formatting the value
// into an error.
func SetKey(l Local, key []byte, now time.Time) error {
	return SetKeys(l, Keyring{Epoch: 1, Keys: map[int][]byte{1: key}}, now)
}

// Keyring is every epoch key held, newest and unwrapped older ones: Keys[Epoch] is
// the newest.
type Keyring struct {
	// Epoch is the newest epoch in Keys.
	Epoch int
	// Keys maps each epoch this machine holds to its key.
	Keys map[int][]byte
}

// SetKeys stores the keyring -- newest under its own name, epoch beside it, one row
// per older epoch -- in one transaction, so no crash leaves a half-unlocked machine.
func SetKeys(l Local, ring Keyring, now time.Time) error {
	newest, err := ring.newest()
	if err != nil {
		return err
	}
	return l.Tx(func(t *db.Tx) error {
		if err := t.SecretPut(SecretKey, newest, now.UTC()); err != nil {
			return fmt.Errorf("sync: set %s: %w", SecretKey, err)
		}
		if err := t.SecretPut(SecretKeyEpoch, []byte(strconv.Itoa(ring.Epoch)), now.UTC()); err != nil {
			return fmt.Errorf("sync: set %s: %w", SecretKeyEpoch, err)
		}
		for _, epoch := range ring.older() {
			if err := t.SecretPut(SecretKeyOldPrefix+strconv.Itoa(epoch), ring.Keys[epoch], now.UTC()); err != nil {
				return fmt.Errorf("sync: set %s%d: %w", SecretKeyOldPrefix, epoch, err)
			}
		}
		return nil
	})
}

// newest is the key the ring's epoch names, refusing no epoch, none held, or any
// above the newest: no unlock produces the last.
func (r Keyring) newest() ([]byte, error) {
	if r.Epoch < 1 {
		return nil, fmt.Errorf("sync: the keyring names no epoch: %w", db.ErrInvalid)
	}
	newest := r.Keys[r.Epoch]
	if len(newest) == 0 {
		return nil, fmt.Errorf("sync: %s is empty: %w", SecretKey, db.ErrInvalid)
	}
	for epoch := range r.Keys {
		if epoch > r.Epoch {
			return nil, fmt.Errorf("sync: the keyring holds epoch %d above its newest %d: %w", epoch, r.Epoch, db.ErrInvalid)
		}
	}
	return newest, nil
}

// older is the epochs below the newest, ascending, written the same way every run.
func (r Keyring) older() []int {
	var out []int
	for epoch := range r.Keys {
		if epoch < r.Epoch {
			out = append(out, epoch)
		}
	}
	sort.Ints(out)
	return out
}

// ReadKeyring returns the stored keyring; ok is false when holding no key.
func ReadKeyring(l Local) (Keyring, bool, error) {
	return ReadKeyringFrom(l)
}

// ReadKeyringFrom is ReadKeyring over the secret-reader surface: a secret-table-only
// caller reads the whole ring without a database handle, older rows by named epoch,
// one row at a time by design.
func ReadKeyringFrom(secrets SecretReader) (Keyring, bool, error) {
	newest, ok, err := secrets.SecretGet(SecretKey)
	if err != nil {
		return Keyring{}, false, fmt.Errorf("sync: read %s: %w", SecretKey, err)
	}
	if !ok {
		return Keyring{}, false, nil
	}
	epoch := 1
	value, ok, err := secrets.SecretGet(SecretKeyEpoch)
	if err != nil {
		return Keyring{}, false, fmt.Errorf("sync: read %s: %w", SecretKeyEpoch, err)
	}
	if ok {
		parsed, err := strconv.Atoi(string(value))
		if err != nil || strconv.Itoa(parsed) != string(value) || parsed < 1 {
			return Keyring{}, false, fmt.Errorf("sync: %s is %q, not an epoch: %w", SecretKeyEpoch, value, db.ErrInvalid)
		}
		epoch = parsed
	}
	ring := Keyring{Epoch: epoch, Keys: map[int][]byte{epoch: newest}}
	for e := 1; e < epoch; e++ {
		key, ok, err := secrets.SecretGet(SecretKeyOldPrefix + strconv.Itoa(e))
		if err != nil {
			return Keyring{}, false, fmt.Errorf("sync: read %s%d: %w", SecretKeyOldPrefix, e, err)
		}
		if ok {
			ring.Keys[e] = key
		}
	}
	return ring, true, nil
}

// ReadKey returns the stored key; ok is false when none.
func ReadKey(l Local) (key []byte, ok bool, err error) {
	return ReadKeyFrom(l)
}

// SecretReader is the one row the key lives in, read without the rest of the
// database: a secret-table-only caller -- a document opening an arriving value --
// reads through this rather than opening a whole handle.
type SecretReader interface {
	SecretGet(name string) ([]byte, bool, error)
}

// ReadKeyFrom is ReadKey over the SecretReader surface.
func ReadKeyFrom(secrets SecretReader) (key []byte, ok bool, err error) {
	key, ok, err = secrets.SecretGet(SecretKey)
	if err != nil {
		return nil, false, fmt.Errorf("sync: read %s: %w", SecretKey, err)
	}
	return key, ok, nil
}

// SigningKey is the key entries sign and verify with, or nil on a locked machine,
// read per attempt so a running unlock takes effect next tick. An unreadable key is
// an error, never locked: running locked would import entries unchecked.
func SigningKey(kv db.KV) ([]byte, error) {
	l, isDB := kv.(Local)
	if !isDB || l == nil {
		return nil, nil
	}
	key, ok, err := ReadKey(l)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return key, nil
}

// DeleteKey removes the stored keyring and the remote's facts; absent is a no-op.
func DeleteKey(l Local) error {
	return l.Tx(func(t *db.Tx) error {
		// Older rows name their epoch, so the epoch reads before anything deletes: a
		// missing epoch row holds no older rows, the ring writing as one unit.
		epoch := 1
		if value, ok, err := t.SecretGet(SecretKeyEpoch); err != nil {
			return fmt.Errorf("sync: read %s: %w", SecretKeyEpoch, err)
		} else if ok {
			parsed, err := strconv.Atoi(string(value))
			if err != nil || strconv.Itoa(parsed) != string(value) || parsed < 1 {
				return fmt.Errorf("sync: %s is %q, not an epoch: %w", SecretKeyEpoch, value, db.ErrInvalid)
			}
			epoch = parsed
		}
		for e := 1; e < epoch; e++ {
			if err := t.SecretDelete(SecretKeyOldPrefix + strconv.Itoa(e)); err != nil {
				return fmt.Errorf("sync: delete %s%d: %w", SecretKeyOldPrefix, e, err)
			}
		}
		if err := t.SecretDelete(SecretKey); err != nil {
			return fmt.Errorf("sync: delete %s: %w", SecretKey, err)
		}
		if err := t.SecretDelete(SecretKeyEpoch); err != nil {
			return fmt.Errorf("sync: delete %s: %w", SecretKeyEpoch, err)
		}
		if err := t.KVDelete(KeyRemoteKDF); err != nil {
			return fmt.Errorf("sync: delete %s: %w", KeyRemoteKDF, err)
		}
		if err := t.KVDelete(SyncRemoteEpoch); err != nil {
			return fmt.Errorf("sync: delete %s: %w", SyncRemoteEpoch, err)
		}
		return nil
	})
}
