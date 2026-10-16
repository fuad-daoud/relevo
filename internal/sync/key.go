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

// SecretKey is the name the derived sync key is stored under, in the local
// secret table beside the token. The passphrase itself is never stored.
const SecretKey = "sync.key"

// KeyRemoteKDF is the local fact that the remote holds key material: 1 or 0.
// It is what lets status say locked without reaching the network.
const KeyRemoteKDF = "sync.remote_kdf"

// SyncRemoteEpoch is the local fact naming the newest epoch the remote holds,
// written by every attempt that could read it and left as it was by one that
// could not.
const SyncRemoteEpoch = "sync.remote_epoch"

// SyncVouchTarget is the machine-local kv fact naming the sequence up to which
// this origin vouches for the tail it appended before learning of a rekey.
const SyncVouchTarget = "sync.vouch_target"

// SecretKeyEpoch is the secret-table row naming the newest epoch this machine
// holds. It sits in the secret table beside the key rather than in the kv facts
// because a caller holding only the secret table -- a document opening an
// arriving value -- reads the keyring through that surface alone.
const SecretKeyEpoch = "sync.key.epoch"

// SecretKeyOldPrefix prefixes the secret-table row holding one older epoch's
// key, named for the epoch it holds.
const SecretKeyOldPrefix = "sync.key.old."

// ErrKDFParams marks a KDF string that is malformed or outside the bounds. The
// remote chooses these numbers and is untrusted, so a huge memory cost is
// refused rather than allocated.
var ErrKDFParams = errors.New("sync: the remote's key derivation parameters are not acceptable")

const (
	kdfName       = "argon2id"
	kdfKeyLen     = 32
	keyCheckLabel = "relevo-sync-key-check-v1"

	kdfMinTime, kdfMaxTime       = 1, 4
	kdfMinMemory, kdfMaxMemory   = 19456, 262144
	kdfMinThreads, kdfMaxThreads = 1, 8
)

// KDFParams are the argon2id cost parameters; MemoryKiB is in kibibytes.
type KDFParams struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

// DefaultKDF is what a new key is derived with.
var DefaultKDF = KDFParams{Time: 3, MemoryKiB: 65536, Threads: 4}

// String formats the parameters as the remote stores them.
func (p KDFParams) String() string {
	return fmt.Sprintf("%s;t=%d;m=%d;p=%d", kdfName, p.Time, p.MemoryKiB, p.Threads)
}

// ParseKDF parses a stored KDF string and refuses anything outside the bounds.
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

// DeriveKey stretches the passphrase into the 32-byte sync key.
func DeriveKey(passphrase, salt []byte, p KDFParams) []byte {
	return argon2.IDKey(passphrase, salt, p.Time, p.MemoryKiB, p.Threads, kdfKeyLen)
}

// KeyCheck is the value the remote holds so another machine can tell a wrong
// passphrase from a right one. Its label differs from the entry label so a
// check can never double as an entry MAC.
func KeyCheck(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(keyCheckLabel))
	return mac.Sum(nil)
}

// SetKey stores the derived key. The value is never formatted into an error.
func SetKey(l Local, key []byte, now time.Time) error {
	return SetKeys(l, Keyring{Epoch: 1, Keys: map[int][]byte{1: key}}, now)
}

// Keyring is every epoch key this machine holds: the newest and the older ones
// it unwrapped off the remote's chain. Keys[Epoch] is the newest.
type Keyring struct {
	// Epoch is the newest epoch in Keys.
	Epoch int
	// Keys maps each epoch this machine holds to its key.
	Keys map[int][]byte
}

// SetKeys stores the keyring: the newest key under its own name -- the name
// every existing reader reads -- the epoch beside it, and one row per older
// epoch. All of it lands in one transaction, so a machine is never half
// unlocked across a crash.
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

// newest is the key the ring's epoch names, refusing a ring that names no
// epoch, holds none for it, or carries an epoch above its newest: an older row
// above the newest is a ring no unlock could have produced.
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

// older is the epochs below the newest, ascending, so the rows are written in
// one order every run writes the same way.
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

// ReadKeyring returns the stored keyring; ok is false when this machine holds
// no key at all.
func ReadKeyring(l Local) (Keyring, bool, error) {
	return ReadKeyringFrom(l)
}

// ReadKeyringFrom is ReadKeyring over the secret-reader surface, so a caller
// holding only the secret table reads the whole ring without a database handle.
// The older rows are read from the epoch the machine names rather than from a
// listing, because the surface is one row at a time by design.
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

// ReadKey returns the stored key; ok is false when this machine has none.
func ReadKey(l Local) (key []byte, ok bool, err error) {
	return ReadKeyFrom(l)
}

// SecretReader is the one row the key lives in, read without the rest of the
// database: a caller holding only the secret table -- a document opening an
// arriving value inside somebody else's transaction -- reads the key through
// this rather than taking a whole database handle it would have to open one for.
type SecretReader interface {
	SecretGet(name string) ([]byte, bool, error)
}

// ReadKeyFrom is ReadKey over that surface.
func ReadKeyFrom(secrets SecretReader) (key []byte, ok bool, err error) {
	key, ok, err = secrets.SecretGet(SecretKey)
	if err != nil {
		return nil, false, fmt.Errorf("sync: read %s: %w", SecretKey, err)
	}
	return key, ok, nil
}

// SigningKey is the key entries are signed and verified with, or nil on a
// locked machine. It is read per attempt, so an unlock while the daemon runs
// takes effect on the next tick. A key that cannot be read is an error, never a
// locked machine: running locked would import entries without checking them.
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

// DeleteKey removes the stored keyring and the facts about the remote's. An
// absent key is a no-op.
func DeleteKey(l Local) error {
	return l.Tx(func(t *db.Tx) error {
		// The older rows are named for the epoch each one holds, so what to
		// delete is read before anything goes: a machine whose epoch row is gone
		// holds no older rows, because the ring is written as one unit.
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
