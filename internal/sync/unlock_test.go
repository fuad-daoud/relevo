package sync

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

func unlockFixture(t *testing.T) (Local, *synclog.MemTransport) {
	t.Helper()
	_, _, local := openSplit(t)
	if err := MarkEnabled(local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	return local, synclog.NewMemTransport("a")
}

func fixedRand(b byte) *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{b}, 64)) }

func TestUnlockRefusesWithoutSaltUnlessNew(t *testing.T) {
	t.Parallel()
	local, log := unlockFixture(t)
	_, err := Unlock(context.Background(), local, log, []byte("pw"), false, fixedRand(1))
	if !errors.Is(err, ErrNoKDF) {
		t.Fatalf("Unlock without a salt = %v, want ErrNoKDF", err)
	}
	if _, ok, _ := ReadKey(local); ok {
		t.Error("a refused unlock stored a key")
	}
}

func TestUnlockNewCreatesSaltAndStoresKey(t *testing.T) {
	t.Parallel()
	local, log := unlockFixture(t)
	created, err := Unlock(context.Background(), local, log, []byte("pw"), true, fixedRand(1))
	if err != nil || !created {
		t.Fatalf("Unlock --new = %v, %v; want created", created, err)
	}
	meta, _ := log.KeyMeta(context.Background())
	if !meta.Present || len(meta.Salt) != 16 || meta.KDF != DefaultKDF.String() {
		t.Fatalf("remote material = %+v", meta)
	}
	key, ok, _ := ReadKey(local)
	if !ok || !bytes.Equal(KeyCheck(key), meta.Check) {
		t.Error("the stored key does not reproduce the remote's check")
	}
	if v, _, _ := local.KVGet(KeyRemoteKDF); string(v) != "1" {
		t.Errorf("remote_kdf = %q, want 1", v)
	}
	if state, _ := KeyState(local); state != "unlocked" {
		t.Errorf("KeyState = %q, want unlocked", state)
	}
}

func TestUnlockNewLosingTheRaceAdoptsTheWinnersSalt(t *testing.T) {
	t.Parallel()
	local, log := unlockFixture(t)
	salt := bytes.Repeat([]byte{9}, 16)
	winner := DeriveKey([]byte("pw"), salt, DefaultKDF)
	other := log.OnLog("b")
	if _, err := other.KeyInit(context.Background(), synclog.KeyMeta{
		Salt: salt, KDF: DefaultKDF.String(), Check: KeyCheck(winner), Present: true,
	}); err != nil {
		t.Fatalf("seed KeyInit: %v", err)
	}
	created, err := Unlock(context.Background(), local, log, []byte("pw"), true, fixedRand(1))
	if err != nil || created {
		t.Fatalf("Unlock --new after a lost race = %v, %v; want adopted, not created", created, err)
	}
	if key, _, _ := ReadKey(local); !bytes.Equal(key, winner) {
		t.Error("the stored key is not the one the winner's salt derives")
	}
}

func TestUnlockNewNeverOverwritesAnExistingSalt(t *testing.T) {
	t.Parallel()
	local, log := unlockFixture(t)
	if _, err := Unlock(context.Background(), local, log, []byte("pw"), true, fixedRand(1)); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}
	before, _ := log.KeyMeta(context.Background())
	second, _ := unlockFixture(t)
	created, err := Unlock(context.Background(), second, log, []byte("pw"), true, fixedRand(2))
	if err != nil || created {
		t.Fatalf("second Unlock --new = %v, %v; want plain unlock", created, err)
	}
	after, _ := log.KeyMeta(context.Background())
	if !bytes.Equal(before.Salt, after.Salt) || !bytes.Equal(before.Check, after.Check) {
		t.Error("--new replaced the remote's existing material")
	}
}

func TestUnlockRefusesAWrongPassphraseAndStoresNothing(t *testing.T) {
	t.Parallel()
	local, log := unlockFixture(t)
	if _, err := Unlock(context.Background(), local, log, []byte("right"), true, fixedRand(1)); err != nil {
		t.Fatalf("setup Unlock: %v", err)
	}
	fresh, _ := unlockFixture(t)
	_, err := Unlock(context.Background(), fresh, log, []byte("wrong"), false, fixedRand(1))
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Unlock with a wrong passphrase = %v, want ErrWrongPassphrase", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("error echoes the passphrase: %v", err)
	}
	if _, ok, _ := ReadKey(fresh); ok {
		t.Error("a wrong passphrase stored a key")
	}
	if _, ok, _ := fresh.KVGet(KeyRemoteKDF); ok {
		t.Error("a wrong passphrase recorded the remote fact")
	}
}

func TestUnlockRefusesWhenSyncIsOff(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	_, err := Unlock(context.Background(), local, synclog.NewMemTransport("a"), []byte("pw"), true, fixedRand(1))
	if !errors.Is(err, ErrSyncOff) {
		t.Fatalf("Unlock with sync off = %v, want ErrSyncOff", err)
	}
}

// epochTwoLog is a remote that has rekeyed: its material is epoch 2's, and the
// epoch 1 key it replaced is stored sealed under epoch 2's key. The rekey that
// wrote it is the real one, so the state a machine unlocks against here is the
// state a rekey leaves behind.
func epochTwoLog(t *testing.T, oldPass, newPass string) *synclog.MemTransport {
	t.Helper()
	log := synclog.NewMemTransport("a")
	salt1 := bytes.Repeat([]byte{7}, saltLen)
	key1 := DeriveKey([]byte(oldPass), salt1, DefaultKDF)
	if _, err := log.KeyInit(context.Background(), synclog.KeyMeta{
		Salt: salt1, KDF: DefaultKDF.String(), Check: KeyCheck(key1), Present: true,
	}); err != nil {
		t.Fatalf("seed epoch 1: %v", err)
	}
	salt2 := bytes.Repeat([]byte{8}, saltLen)
	key2 := DeriveKey([]byte(newPass), salt2, DefaultKDF)
	wrapped, err := WrapKey(key2, 1, key1)
	if err != nil {
		t.Fatalf("wrap epoch 1: %v", err)
	}
	if _, err := log.KeyRekey(context.Background(), synclog.KeyRekeySpec{
		From: 1,
		Key: synclog.KeyMeta{
			Salt: salt2, KDF: DefaultKDF.String(), Check: KeyCheck(key2),
			Present: true, Epoch: 2, Wrapped: map[int][]byte{1: wrapped},
		},
	}); err != nil {
		t.Fatalf("rekey to epoch 2: %v", err)
	}
	return log
}

// TestUnlockOnARekeyedRemoteStoresTheWholeKeyring pins what an unlock is worth
// after a rekey: the newest key and every older one the remote's chain unwraps
// to, so this machine reads epoch 1's history and signs under the newest key.
func TestUnlockOnARekeyedRemoteStoresTheWholeKeyring(t *testing.T) {
	t.Parallel()
	local, _ := unlockFixture(t)
	log := epochTwoLog(t, "the old passphrase", "the new passphrase")
	if _, err := Unlock(context.Background(), local, log, []byte("the new passphrase"), false, fixedRand(1)); err != nil {
		t.Fatalf("Unlock on a rekeyed remote: %v", err)
	}
	ring, ok, err := ReadKeyring(local)
	if err != nil || !ok {
		t.Fatalf("ReadKeyring after the unlock = %+v, %v; want the keyring", ring, err)
	}
	if ring.Epoch != 2 {
		t.Errorf("epoch = %d, want 2", ring.Epoch)
	}
	salt2 := bytes.Repeat([]byte{8}, saltLen)
	if !bytes.Equal(ring.Keys[2], DeriveKey([]byte("the new passphrase"), salt2, DefaultKDF)) {
		t.Error("the newest key is not the one the new passphrase derives")
	}
	salt1 := bytes.Repeat([]byte{7}, saltLen)
	if !bytes.Equal(ring.Keys[1], DeriveKey([]byte("the old passphrase"), salt1, DefaultKDF)) {
		t.Error("the older key is not the one the remote's chain unwraps to")
	}
}

// TestUnlockOnARekeyedRemoteRefusesTheOldPassphrase pins that an old passphrase
// is refused rather than misread: the remote's check is the new key's, so the
// passphrase that opened epoch 1 opens nothing now and stores nothing.
func TestUnlockOnARekeyedRemoteRefusesTheOldPassphrase(t *testing.T) {
	t.Parallel()
	fresh, _ := unlockFixture(t)
	log := epochTwoLog(t, "the old passphrase", "the new passphrase")
	_, err := Unlock(context.Background(), fresh, log, []byte("the old passphrase"), false, fixedRand(1))
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Unlock with the old passphrase = %v, want ErrWrongPassphrase", err)
	}
	if _, ok, _ := ReadKey(fresh); ok {
		t.Error("a refused unlock stored a key")
	}
	if _, ok, _ := fresh.KVGet(KeyRemoteKDF); ok {
		t.Error("a refused unlock recorded the remote fact")
	}
}
