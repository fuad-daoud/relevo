package sync

import (
	"bytes"
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrNoKDF is a plain unlock against a remote that holds no key material.
var ErrNoKDF = errors.New("sync: the remote holds no key yet: run `relevo db sync unlock --new` to create one")

// ErrWrongPassphrase is a passphrase whose derived key does not match the
// remote's check. Nothing is stored when it is returned.
var ErrWrongPassphrase = errors.New("sync: that passphrase does not match the remote's key")

// ErrSyncOff is an unlock on a machine that is not syncing.
var ErrSyncOff = fmt.Errorf("sync: sync is off on this machine: %w", db.ErrInvalid)

const saltLen = 16

// Unlock derives the key from the passphrase and stores it once it matches
// the remote's check. With isNew and no remote salt it creates the material
// first; with a salt already there it never overwrites, and a machine that
// loses the creation race checks against the winner's. created reports
// whether this call created the material.
func Unlock(ctx context.Context, local Local, transport synclog.LogTransport, passphrase []byte, isNew bool, rand io.Reader) (created bool, err error) {
	on, err := Enabled(local)
	if err != nil {
		return false, err
	}
	if !on {
		return false, ErrSyncOff
	}
	meta, err := transport.KeyMeta(ctx)
	if err != nil {
		return false, fmt.Errorf("sync: unlock: read the remote key: %w", err)
	}
	if !meta.Present {
		if !isNew {
			return false, ErrNoKDF
		}
		if meta, created, err = initKey(ctx, transport, passphrase, rand); err != nil {
			return false, err
		}
	}
	key, err := checkedKey(meta, passphrase)
	if err != nil {
		return false, err
	}
	ring, err := unwrapKeyring(meta, key)
	if err != nil {
		return false, fmt.Errorf("sync: unlock: %w", err)
	}
	oldRing, hadRing, err := ReadKeyring(local)
	if err != nil {
		return false, fmt.Errorf("sync: unlock: read existing keyring: %w", err)
	}
	now := time.Now()
	if err := SetKeys(local, ring, now); err != nil {
		return false, err
	}
	if err := local.KVPut(KeyRemoteKDF, []byte("1")); err != nil {
		return false, fmt.Errorf("sync: unlock: %w", err)
	}
	if err := local.KVPut(SyncKeyCheckMatch, []byte("1")); err != nil {
		return false, fmt.Errorf("sync: unlock: %w", err)
	}
	if hadRing && ring.Epoch > oldRing.Epoch {
		if head, ok, err := synclog.OwnHead(local); err == nil && ok {
			if err := local.KVPut(SyncVouchTarget, []byte(strconv.Itoa(head))); err != nil {
				return false, fmt.Errorf("sync: unlock: record the vouch target: %w", err)
			}
		}
	} else {
		_ = local.KVDelete(SyncVouchTarget)
	}
	return created, nil
}

// unwrapKeyring builds the ring the remote's chain unwraps to: the key the
// passphrase derived for the newest epoch, and every older epoch's key out of
// the rows sealed under the one that replaced it. A chain with a missing or
// unopenable row is a refusal, never a shorter ring -- a machine that stored
// half a chain would read history it cannot verify as though it could.
func unwrapKeyring(meta synclog.KeyMeta, newest []byte) (Keyring, error) {
	ring := Keyring{Epoch: meta.Epoch, Keys: map[int][]byte{meta.Epoch: newest}}
	newer := newest
	for epoch := meta.Epoch - 1; epoch >= 1; epoch-- {
		wrapped, ok := meta.Wrapped[epoch]
		if !ok {
			return Keyring{}, fmt.Errorf("sync: the remote holds no wrapped key for epoch %d: %w", epoch, ErrKDFParams)
		}
		key, err := UnwrapKey(newer, epoch, wrapped)
		if err != nil {
			return Keyring{}, err
		}
		ring.Keys[epoch] = key
		newer = key
	}
	return ring, nil
}

// initKey writes fresh material and answers with what the remote holds
// afterwards, which is another machine's when it won the race; ours says
// whether it is this call's.
func initKey(ctx context.Context, transport synclog.LogTransport, passphrase []byte, rand io.Reader) (held synclog.KeyMeta, ours bool, err error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand, salt); err != nil {
		return synclog.KeyMeta{}, false, fmt.Errorf("sync: unlock: make a salt: %w", err)
	}
	key := DeriveKey(passphrase, salt, DefaultKDF)
	held, err = transport.KeyInit(ctx, synclog.KeyMeta{
		Salt: salt, KDF: DefaultKDF.String(), Check: KeyCheck(key), Present: true,
	})
	if err != nil {
		return synclog.KeyMeta{}, false, fmt.Errorf("sync: unlock: write the remote key: %w", err)
	}
	return held, bytes.Equal(held.Salt, salt), nil
}

// checkedKey derives the key from the remote's salt and parameters and returns
// it only when it reproduces the remote's check.
func checkedKey(meta synclog.KeyMeta, passphrase []byte) ([]byte, error) {
	if !meta.Present || len(meta.Salt) == 0 {
		return nil, fmt.Errorf("%w: the remote's key has no salt", ErrKDFParams)
	}
	params, err := ParseKDF(meta.KDF)
	if err != nil {
		return nil, err
	}
	key := DeriveKey(passphrase, meta.Salt, params)
	if !hmac.Equal(KeyCheck(key), meta.Check) {
		return nil, ErrWrongPassphrase
	}
	return key, nil
}

// VerifyJoinKey is the check an enable --join runs before it stores anything: it
// reads the remote's key material and returns the keyring the remote's chain
// unwraps to, only when the passphrase reproduces the remote's check.
//
// It exists rather than reusing Unlock because Unlock refuses a machine that is
// not syncing -- ErrSyncOff -- and a joiner is by definition not syncing yet: it
// is the thing that turns sync on. What the two share is checkedKey, which is
// where the passphrase is actually compared, so the answer to "is this the right
// passphrase" is written once.
//
// A remote holding no key is ErrNoKDF, the same refusal `unlock` gives. A joiner
// that was handed such a remote could not unlock what it imported and would end
// up holding nothing, so it is refused here rather than after a join that
// appeared to work.
func VerifyJoinKey(ctx context.Context, local Local, transport synclog.LogTransport, passphrase []byte) (Keyring, error) {
	meta, err := transport.KeyMeta(ctx)
	if err != nil {
		return Keyring{}, fmt.Errorf("sync: join: read the remote key: %w", err)
	}
	if !meta.Present {
		return Keyring{}, ErrNoKDF
	}
	key, err := checkedKey(meta, passphrase)
	if err != nil {
		return Keyring{}, err
	}
	ring, err := unwrapKeyring(meta, key)
	if err != nil {
		return Keyring{}, fmt.Errorf("sync: join: %w", err)
	}
	if err := recordRemoteKeyFact(local, meta); err != nil {
		return Keyring{}, err
	}
	return ring, nil
}

// recordRemoteKeyFact writes what the remote holds so status can report a
// locked machine without a round trip. It is the writing half of RecordRemoteKDF,
// split out for the caller that has just asked the remote rather than asking
// again.
func recordRemoteKeyFact(kv db.KV, meta synclog.KeyMeta) error {
	value := []byte("0")
	if meta.Present {
		value = []byte("1")
	}
	if err := kv.KVPut(KeyRemoteKDF, value); err != nil {
		return fmt.Errorf("sync: join: record the remote key: %w", err)
	}
	return nil
}

// RecordRemoteKDF learns whether the remote holds key material and records it
// as the local fact status reads, beside the remote's newest epoch. A failed
// read leaves the recorded values as they were, so an unreachable remote never
// flips a machine between locked and none.
func RecordRemoteKDF(ctx context.Context, kv db.KV, transport synclog.LogTransport) {
	meta, err := transport.KeyMeta(ctx)
	if err != nil {
		return
	}
	value := []byte("0")
	if meta.Present {
		value = []byte("1")
	}
	if err := kv.KVPut(KeyRemoteKDF, value); err != nil {
		slog.Warn("sync: record the remote key fact", "err", err)
	}
	if err := kv.KVPut(SyncRemoteEpoch, []byte(strconv.Itoa(meta.Epoch))); err != nil {
		slog.Warn("sync: record the remote epoch", "err", err)
	}
	if ring, ok, err := heldKeyring(kv); err == nil && ok && meta.Present && ring.Epoch == meta.Epoch {
		match := "0"
		if hmac.Equal(KeyCheck(ring.Keys[ring.Epoch]), meta.Check) {
			match = "1"
		}
		if err := kv.KVPut(SyncKeyCheckMatch, []byte(match)); err != nil {
			slog.Warn("sync: record the key check match", "err", err)
		}
	}
}

// KeyState is the key line status reports: unlocked when this machine holds a
// key that names the remote's newest epoch, stale when it holds one below the
// recorded remote epoch -- the passphrase changed and this machine is on the
// old key -- locked when it holds none and the remote does, none otherwise.
func KeyState(l Local) (string, error) {
	ring, ok, err := ReadKeyring(l)
	if err != nil {
		return "", err
	}
	if ok {
		remote, ok, err := remoteEpoch(l)
		if err != nil {
			return "", err
		}
		if ok && ring.Epoch < remote {
			return "stale", nil
		}
		if ok && ring.Epoch == remote && checkMismatch(l) {
			return "stale", nil
		}
		return "unlocked", nil
	}
	value, ok, err := l.KVGet(KeyRemoteKDF)
	if err != nil {
		return "", fmt.Errorf("sync: read %s: %w", KeyRemoteKDF, err)
	}
	if ok && string(bytes.TrimSpace(value)) == "1" {
		return "locked", nil
	}
	return "none", nil
}

// checkMismatch reports whether the machine-local fact records that the held
// newest key's check does not match the remote's check at the same epoch.
func checkMismatch(l db.KV) bool {
	value, ok, err := l.KVGet(SyncKeyCheckMatch)
	if err != nil || !ok {
		return false
	}
	return string(bytes.TrimSpace(value)) == "0"
}

// remoteEpoch reads the local fact naming the remote's newest epoch, written
// by every attempt that could read the remote and left as it was by one that
// could not. A row that does not parse reads as absent: it is this machine's
// own row, and the worst a broken one costs is a stale machine that reports
// unlocked until the next attempt rewrites it.
func remoteEpoch(l db.KV) (int, bool, error) {
	value, ok, err := l.KVGet(SyncRemoteEpoch)
	if err != nil {
		return 0, false, fmt.Errorf("sync: read %s: %w", SyncRemoteEpoch, err)
	}
	if !ok {
		return 0, false, nil
	}
	epoch, err := strconv.Atoi(string(bytes.TrimSpace(value)))
	if err != nil || epoch < 1 {
		return 0, false, nil
	}
	return epoch, true, nil
}

// DrainParkedDocs applies the doc entries parked while this machine held no
// key, now that Unlock has stored one. It verifies each against the key and
// refuses what does not verify, so the unlock is when a forged entry parked
// earlier is found out rather than when the next tick happens to run.
func DrainParkedDocs(shared *db.DB, local Local, transport synclog.LogTransport) (synclog.ImportResult, error) {
	if shared == nil {
		return synclog.ImportResult{}, nil
	}
	ring, ok, err := heldKeyring(local)
	if err != nil || !ok {
		return synclog.ImportResult{}, err
	}
	imp := synclog.NewImporter(shared, transport)
	imp.SetKeyring(synclog.Keyring{Epoch: ring.Epoch, Keys: ring.Keys})
	BindDocs(imp, shared)
	// The unlock drains what the lock parked, and the capture runs first so a
	// script a person edited while this machine held no key travels with the
	// drain rather than with whichever attempt comes after it.
	CaptureFiles(shared)
	res, err := imp.DrainPending()
	// The settle runs after the drain, not inside it: what it renders is the
	// config the drain wrote, which is only true once it has committed.
	SettleFiles(shared)
	return res, err
}
