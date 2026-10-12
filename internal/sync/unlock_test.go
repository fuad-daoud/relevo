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
