package sync

// The rekey: one act that rotates the log's key to a new epoch, cuts every
// origin's history at the head the rekeyer observed, stores the whole keyring
// on this machine, and signals the secrets document so the values it holds are
// re-sealed under the newest key on the next export.
//
// The order is the contract. The machine must be syncing and unlocked; the
// current passphrase is checked against the remote's check before anything is
// written, so a wrong one changes nothing anywhere; the new one may not be the
// current one, because rotation answers a leaked passphrase and the same
// passphrase against the new public salt lets the leak re-derive the new key;
// and the write itself is the transport's one transaction -- fresh material,
// the key it replaced sealed for the machines still holding it, and one cut per
// origin -- which refuses whole when another machine rekeyed first or a head
// moved under the caller.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrMachineLocked is a rekey on a machine that holds no sync key. The fix
// names the verb: unlock first, with the current passphrase.
var ErrMachineLocked = errors.New("sync: this machine is locked: run relevo db sync unlock before rekeying")

// ErrEpochRace is a rekey that lost the compare-and-set: another machine
// rekeyed first. Nothing of this caller's landed, and its key is an old one
// now -- the fix is the winner's passphrase, at unlock.
var ErrEpochRace = errors.New("sync: another machine rekeyed first, this machine now holds an old key: run relevo db sync unlock with the winner's new passphrase")

// ErrSamePassphrase is a rotation to the passphrase it is rotating away from.
// The same passphrase against the new public salt derives the same key, so it
// would spend the epoch without retiring anything.
var ErrSamePassphrase = fmt.Errorf("sync: the new passphrase is the current one: rotation needs a different passphrase: %w", db.ErrInvalid)

// ErrEmptyPassphrase is a rotation to the empty passphrase.
var ErrEmptyPassphrase = fmt.Errorf("sync: the new passphrase is empty: %w", db.ErrInvalid)

// Rekey rotates the log's key from the epoch the remote holds to the next one.
// It stores nothing until the remote's transaction has accepted the whole
// write, and it stores everything -- the newest key, every older key the
// remote's chain unwraps to, and the facts about the remote -- as one act, so
// a crash between the remote and the local store leaves the machine stale, the
// ordinary state, and unlock recovers it.
func Rekey(ctx context.Context, local Local, transport synclog.LogTransport, current, newPass []byte, rand io.Reader) error {
	if len(newPass) == 0 {
		return ErrEmptyPassphrase
	}
	on, err := Enabled(local)
	if err != nil {
		return err
	}
	if !on {
		return ErrSyncOff
	}
	if _, ok, err := ReadKeyring(local); err != nil {
		return err
	} else if !ok {
		return ErrMachineLocked
	}
	meta, err := transport.KeyMeta(ctx)
	if err != nil {
		return fmt.Errorf("sync: rekey: read the remote key: %w", err)
	}
	if !meta.Present {
		return ErrNoKDF
	}
	old, err := checkedKey(meta, current)
	if err != nil {
		return err
	}
	if bytes.Equal(newPass, current) {
		return ErrSamePassphrase
	}
	return rotateKey(ctx, local, transport, meta, old, newPass, rand)
}

// rotateKey writes the new epoch: the fresh material, the sealed old key, and
// one cut per origin at the head this machine observed, then the keyring, the
// facts, and the signal that re-seals this machine's secrets.
func rotateKey(ctx context.Context, local Local, transport synclog.LogTransport, meta synclog.KeyMeta, old, newPass []byte, rand io.Reader) error {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand, salt); err != nil {
		return fmt.Errorf("sync: rekey: make a salt: %w", err)
	}
	newKey := DeriveKey(newPass, salt, DefaultKDF)
	wrapped, err := WrapKey(newKey, meta.Epoch, old)
	if err != nil {
		return fmt.Errorf("sync: rekey: %w", err)
	}
	state, err := transport.ReadMarks()
	if err != nil {
		return fmt.Errorf("sync: rekey: read the heads the cuts sit at: %w", err)
	}
	spec := rekeySpec(meta, newKey, salt, wrapped, state.Heads)
	answer, err := transport.KeyRekey(ctx, spec)
	if err != nil {
		return rekeyRefused(ctx, transport, spec.From, err)
	}
	// The answer is what the remote holds afterwards, which is another
	// machine's when this one lost: an epoch or key check that is not what
	// this caller wrote says so, and nothing here is stored.
	if answer.Epoch != spec.Key.Epoch || !bytes.Equal(answer.Check, spec.Key.Check) {
		return ErrEpochRace
	}
	return storeNewKeyring(local, answer, newKey)
}

// rekeySpec is what the transport is asked to write: the epoch it is rotating
// from, the new material with the key it replaced sealed for holders of it,
// and one cut per origin in the log, at the head given, tagged under the new
// key. The transport verifies every cut against the log's own heads before
// anything is written, so a stale observation refuses whole.
func rekeySpec(meta synclog.KeyMeta, newKey, salt, wrapped []byte, heads map[string]int) synclog.KeyRekeySpec {
	epoch := meta.Epoch + 1
	cuts := make(map[string]synclog.EpochCut, len(heads))
	for origin, head := range heads {
		cuts[origin] = synclog.EpochCut{
			Epoch: epoch,
			Seq:   head,
			Tag:   synclog.EpochCutTag(newKey, origin, epoch, head),
		}
	}
	return synclog.KeyRekeySpec{
		From: meta.Epoch,
		Key: synclog.KeyMeta{
			Salt: salt, KDF: DefaultKDF.String(), Check: KeyCheck(newKey),
			Present: true, Epoch: epoch,
			Wrapped: map[int][]byte{meta.Epoch: wrapped},
		},
		Cuts: cuts,
	}
}

// rekeyRefused classifies a refusal the transport answered: an epoch that has
// moved past the one this caller read is the race, whatever words carried it,
// and anything else is the transport's own reason.
func rekeyRefused(ctx context.Context, transport synclog.LogTransport, from int, err error) error {
	if errors.Is(err, ErrEpochRace) {
		return ErrEpochRace
	}
	if meta, merr := transport.KeyMeta(ctx); merr == nil && meta.Epoch > from {
		return ErrEpochRace
	}
	return fmt.Errorf("sync: rekey: %w", err)
}

// storeNewKeyring lands what the accepted rekey earned: the whole chain the
// remote's answer unwraps to -- which is also the check that the chain the
// remote holds actually opens -- the facts about the remote, and the signal
// that re-seals this machine's secrets under the newest key on the next
// export. The signal carries no stamp: the document's next export decides
// every key's rank, and the stamp rule is the exporter's.
func storeNewKeyring(local Local, answer synclog.KeyMeta, newKey []byte) error {
	ring, err := unwrapKeyring(answer, newKey)
	if err != nil {
		return fmt.Errorf("sync: rekey: %w", err)
	}
	if err := SetKeys(local, ring, time.Now()); err != nil {
		return err
	}
	if err := local.KVPut(KeyRemoteKDF, []byte("1")); err != nil {
		return fmt.Errorf("sync: rekey: %w", err)
	}
	if err := local.KVPut(SyncRemoteEpoch, []byte(fmt.Sprint(answer.Epoch))); err != nil {
		return fmt.Errorf("sync: rekey: %w", err)
	}
	if err := local.SignalDoc(SecretsDocName); err != nil {
		return fmt.Errorf("sync: rekey: signal the secrets document: %w", err)
	}
	return nil
}
