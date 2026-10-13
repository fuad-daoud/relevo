package sync

import (
	"bytes"
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	now := time.Now()
	if err := SetKey(local, key, now); err != nil {
		return false, err
	}
	if err := local.KVPut(KeyRemoteKDF, []byte("1")); err != nil {
		return false, fmt.Errorf("sync: unlock: %w", err)
	}
	return created, nil
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

// RecordRemoteKDF learns whether the remote holds key material and records it
// as the local fact status reads. A failed read leaves the recorded value as it
// was, so an unreachable remote never flips a machine between locked and none.
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
}

// KeyState is the key line status reports: unlocked when this machine holds a
// key, locked when it holds none and the remote does, none otherwise.
func KeyState(l Local) (string, error) {
	_, ok, err := ReadKey(l)
	if err != nil {
		return "", err
	}
	if ok {
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

// DrainParkedDocs applies the doc entries parked while this machine held no
// key, now that Unlock has stored one. It verifies each against the key and
// refuses what does not verify, so the unlock is when a forged entry parked
// earlier is found out rather than when the next tick happens to run.
func DrainParkedDocs(shared *db.DB, local Local, transport synclog.LogTransport) (synclog.ImportResult, error) {
	key, err := SigningKey(local)
	if err != nil || len(key) == 0 || shared == nil {
		return synclog.ImportResult{}, err
	}
	imp := synclog.NewImporter(shared, transport)
	imp.SetKey(key)
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
