package sync

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
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
	if len(key) == 0 {
		return fmt.Errorf("sync: %s is empty: %w", SecretKey, db.ErrInvalid)
	}
	return l.Tx(func(t *db.Tx) error {
		if err := t.SecretPut(SecretKey, key, now.UTC()); err != nil {
			return fmt.Errorf("sync: set %s: %w", SecretKey, err)
		}
		return nil
	})
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

// DeleteKey removes the stored key and the fact about the remote's. An absent
// key is a no-op.
func DeleteKey(l Local) error {
	return l.Tx(func(t *db.Tx) error {
		if err := t.SecretDelete(SecretKey); err != nil {
			return fmt.Errorf("sync: delete %s: %w", SecretKey, err)
		}
		if err := t.KVDelete(KeyRemoteKDF); err != nil {
			return fmt.Errorf("sync: delete %s: %w", KeyRemoteKDF, err)
		}
		return nil
	})
}
