package sync

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func keyringFixture(t *testing.T) Local {
	t.Helper()
	_, _, local := openSplit(t)
	return local
}

func threeEpochRing() Keyring {
	return Keyring{Epoch: 3, Keys: map[int][]byte{
		1: []byte("epoch one's key"),
		2: []byte("epoch two's key"),
		3: []byte("epoch three's key"),
	}}
}

// TestSetKeysRoundTripsThroughTheSecretTable pins the ring's rows: the newest
// key under the name every existing reader reads, the epoch beside it, and one
// row per older epoch -- all read back as the ring that was stored.
func TestSetKeysRoundTripsThroughTheSecretTable(t *testing.T) {
	t.Parallel()
	local := keyringFixture(t)
	ring := threeEpochRing()
	if err := SetKeys(local, ring, time.Now()); err != nil {
		t.Fatalf("SetKeys: %v", err)
	}
	got, ok, err := ReadKeyring(local)
	if err != nil || !ok {
		t.Fatalf("ReadKeyring = %+v, %v; want the stored ring", got, err)
	}
	if got.Epoch != ring.Epoch || len(got.Keys) != len(ring.Keys) {
		t.Fatalf("ReadKeyring = %+v, want %+v", got, ring)
	}
	for epoch, key := range ring.Keys {
		if !bytes.Equal(got.Keys[epoch], key) {
			t.Errorf("epoch %d = %q, want %q", epoch, got.Keys[epoch], key)
		}
	}
	// The newest key is where the single-key readers read it, which is what
	// keeps every existing caller reading the machine as unlocked.
	key, ok, err := ReadKey(local)
	if err != nil || !ok || !bytes.Equal(key, ring.Keys[ring.Epoch]) {
		t.Errorf("ReadKey = %q, %v; want the newest epoch's key", key, err)
	}
}

// TestReadKeyringOnAMachineThatHoldsNoKey pins that a machine with no key reads
// as holding none, not as an error and not as an empty ring with an epoch.
func TestReadKeyringOnAMachineThatHoldsNoKey(t *testing.T) {
	t.Parallel()
	local := keyringFixture(t)
	ring, ok, err := ReadKeyring(local)
	if err != nil || ok {
		t.Fatalf("ReadKeyring on a locked machine = %+v, %v; want none", ring, err)
	}
}

// TestSetKeysRefusesARingNoUnlockCouldProduce pins the ring's own shape: a
// newest key it names, and nothing above that newest.
func TestSetKeysRefusesARingNoUnlockCouldProduce(t *testing.T) {
	t.Parallel()
	local := keyringFixture(t)
	for name, ring := range map[string]Keyring{
		"no epoch":        {Keys: map[int][]byte{1: []byte("k")}},
		"no newest key":   {Epoch: 2, Keys: map[int][]byte{1: []byte("k")}},
		"epoch above":     {Epoch: 2, Keys: map[int][]byte{2: []byte("k"), 5: []byte("k")}},
		"an empty newest": {Epoch: 1, Keys: map[int][]byte{1: nil}},
	} {
		if err := SetKeys(local, ring, time.Now()); !errors.Is(err, db.ErrInvalid) {
			t.Errorf("%s: SetKeys = %v, want a refusal naming the ring", name, err)
		}
	}
}

// TestDeleteKeyRemovesTheWholeRing pins that a disable leaves no key behind:
// the newest, the epoch, every older row, and the two facts about the remote.
func TestDeleteKeyRemovesTheWholeRing(t *testing.T) {
	t.Parallel()
	local := keyringFixture(t)
	if err := SetKeys(local, threeEpochRing(), time.Now()); err != nil {
		t.Fatalf("SetKeys: %v", err)
	}
	if err := local.KVPut(KeyRemoteKDF, []byte("1")); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	if err := local.KVPut(SyncRemoteEpoch, []byte("3")); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	if err := DeleteKey(local); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if _, ok, _ := ReadKey(local); ok {
		t.Error("the newest key survived DeleteKey")
	}
	if _, ok, _ := local.SecretGet(SecretKeyEpoch); ok {
		t.Error("the epoch row survived DeleteKey")
	}
	for _, epoch := range []int{1, 2} {
		if _, ok, _ := local.SecretGet(SecretKeyOldPrefix + strconv.Itoa(epoch)); ok {
			t.Errorf("the older row for epoch %d survived DeleteKey", epoch)
		}
	}
	if _, ok, _ := local.KVGet(KeyRemoteKDF); ok {
		t.Error("the remote-kdf fact survived DeleteKey")
	}
	if _, ok, _ := local.KVGet(SyncRemoteEpoch); ok {
		t.Error("the remote-epoch fact survived DeleteKey")
	}
}

// TestTheKeyringRowsAreNotSecretsThatTravel pins the allow-list both ways: the
// keyring's rows are this machine's own, so they are refused on import and
// never exported, on top of the token and the R2 credentials the list already
// keeps home.
func TestTheKeyringRowsAreNotSecretsThatTravel(t *testing.T) {
	t.Parallel()
	for _, name := range []string{SecretKeyEpoch, SecretKeyOldPrefix + "1", SecretKey} {
		if AllowSecret(name) {
			t.Errorf("%s travels, and it must not: it is this machine's own", name)
		}
	}
	if slices.Contains(AllowedSecrets(), SecretKeyEpoch) || slices.Contains(AllowedSecrets(), SecretKeyOldPrefix+"1") {
		t.Error("the keyring's rows are on the list of secrets that travel")
	}
}
