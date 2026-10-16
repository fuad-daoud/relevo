package sync

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests are the snapshot half of the rule that a refused join leaves the
// machine as it was, and the check a join runs before it stores anything. They
// drive both over a real local file and the in-memory log, because the claim is
// about the bytes on disk and a fake store could not show a row written back in
// a different shape than it was read.

// joinRows is every row the snapshot reads, as the bytes a reader would find. An
// absent row reads as a word rather than an empty string so that "the row was
// deleted" and "the row came back with nothing in it" cannot look the same.
func joinRows(t *testing.T, l Local) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, read := range map[string]func() ([]byte, bool, error){
		"section":            func() ([]byte, bool, error) { return l.ConfigGet(SectionSettings) },
		"token":              func() ([]byte, bool, error) { return l.SecretGet(SecretToken) },
		"sync.key":           func() ([]byte, bool, error) { return l.SecretGet(SecretKey) },
		KeyRemoteKDF:         func() ([]byte, bool, error) { return l.KVGet(KeyRemoteKDF) },
		KeyEnabled:           func() ([]byte, bool, error) { return l.KVGet(KeyEnabled) },
		KeyJoin:              func() ([]byte, bool, error) { return l.KVGet(KeyJoin) },
		SecretClientKey:      func() ([]byte, bool, error) { return l.SecretGet(SecretClientKey) },
		SecretTypesafe:       func() ([]byte, bool, error) { return l.SecretGet(SecretTypesafe) },
		SecretR2Endpoint:     func() ([]byte, bool, error) { return l.SecretGet(SecretR2Endpoint) },
		SecretR2Bucket:       func() ([]byte, bool, error) { return l.SecretGet(SecretR2Bucket) },
		SecretR2KeyID:        func() ([]byte, bool, error) { return l.SecretGet(SecretR2KeyID) },
		SecretR2Secret:       func() ([]byte, bool, error) { return l.SecretGet(SecretR2Secret) },
		KeyClientKeyReplaced: func() ([]byte, bool, error) { return l.ConfigGet(KeyClientKeyReplaced) },
	} {
		value, present, err := read()
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if present {
			out[name] = string(value)
			continue
		}
		out[name] = "<absent>"
	}
	return out
}

func joinRowsEqual(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for name, want := range before {
		if after[name] != want {
			t.Errorf("%s: %s = %q after %s, want %q", what, name, after[name], what, want)
		}
	}
}

// A snapshot of a machine holding nothing restores to a machine holding nothing.
//
// The absence has to come back as an absence. A row written back empty is a row
// the preflight reads, and "this machine has no token" is not the same fact as
// "this machine has a token whose value is empty".
func TestJoinSnapshotRestoresAMachineThatHeldNothing(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	before := joinRows(t, local)

	snapshot, err := SnapshotJoin(local)
	if err != nil {
		t.Fatalf("SnapshotJoin: %v", err)
	}
	// Exactly the two rows a join stages, plus the three it would go on to write.
	if err := PutSettings(local, Settings{RemoteURL: "libsql://somewhere.invalid"}, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := SetToken(local, []byte("A-TOKEN"), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := SetKey(local, []byte("A-KEY"), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := local.KVPut(KeyRemoteKDF, []byte("1")); err != nil {
		t.Fatalf("KVPut %s: %v", KeyRemoteKDF, err)
	}
	if err := local.KVPut(KeyEnabled, []byte("true")); err != nil {
		t.Fatalf("KVPut %s: %v", KeyEnabled, err)
	}
	if joinRows(t, local)["section"] == before["section"] {
		t.Fatal("the staging wrote nothing, so this proves nothing")
	}

	if err := snapshot.Restore(local, time.Now()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	joinRowsEqual(t, "a restore", before, joinRows(t, local))
}

// The same rule over rows that already existed.
//
// Restoring over a default is not restoring: a machine that had a remote and a
// token must still have both, byte for byte, after the join it asked for was
// refused.
func TestJoinSnapshotRestoresOverRowsThatAlreadyExisted(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	at := time.Unix(0, 0).UTC()
	if err := PutSettings(local, Settings{RemoteURL: "libsql://was-here.invalid"}, at); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := SetToken(local, []byte("THE-TOKEN-THIS-MACHINE-HAD"), at); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := SetKey(local, []byte("THE-KEY-THIS-MACHINE-HAD"), at); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	// The secrets the fleet's document may land on, held the way an arriving
	// value would be, and the fact that this machine has had a client key
	// replaced: the restore has to put bytes back over rows that were there.
	if err := local.Tx(func(t *db.Tx) error {
		if err := t.SecretPut(SecretClientKey, []byte("THE-CLIENT-KEY-THIS-MACHINE-HAD"), at); err != nil {
			return err
		}
		if err := t.SecretPut(SecretTypesafe, []byte("THE-TYPESAFE-THIS-MACHINE-HAD"), at); err != nil {
			return err
		}
		return t.ConfigPut(KeyClientKeyReplaced, []byte(`{"from":"old","to":"now"}`), at)
	}); err != nil {
		t.Fatalf("write the held secret rows: %v", err)
	}
	if err := local.KVPut(KeyRemoteKDF, []byte("0")); err != nil {
		t.Fatalf("KVPut %s: %v", KeyRemoteKDF, err)
	}
	if err := local.KVPut(KeyEnabled, []byte("false")); err != nil {
		t.Fatalf("KVPut %s: %v", KeyEnabled, err)
	}
	before := joinRows(t, local)

	snapshot, err := SnapshotJoin(local)
	if err != nil {
		t.Fatalf("SnapshotJoin: %v", err)
	}
	if err := PutSettings(local, Settings{RemoteURL: "libsql://moved.invalid"}, time.Now()); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := SetToken(local, []byte("A-DIFFERENT-TOKEN"), time.Now()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := SetKey(local, []byte("A-DIFFERENT-KEY"), time.Now()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}

	if err := snapshot.Restore(local, time.Now()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	joinRowsEqual(t, "a restore", before, joinRows(t, local))
}

// The restore is not a consumption: a snapshot can be put back more than once,
// because a join can fail after the rows were put back once already.
func TestJoinSnapshotRestoresMoreThanOnce(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	at := time.Unix(0, 0).UTC()
	if err := PutSettings(local, Settings{RemoteURL: "libsql://was-here.invalid"}, at); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	snapshot, err := SnapshotJoin(local)
	if err != nil {
		t.Fatalf("SnapshotJoin: %v", err)
	}
	for i := range 2 {
		if err := PutSettings(local, Settings{RemoteURL: "libsql://moved.invalid"}, time.Now()); err != nil {
			t.Fatalf("PutSettings: %v", err)
		}
		if err := snapshot.Restore(local, time.Now()); err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
	}
	settings, err := ReadSettings(local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	if settings.RemoteURL != "libsql://was-here.invalid" {
		t.Errorf("remote = %q after two restores, want the one this machine had", settings.RemoteURL)
	}
}

// VerifyJoinKey is the check a join runs before it stores anything, and it has no
// enabled mark to get past: a joiner is by definition not syncing yet. Unlock
// refuses such a machine outright with ErrSyncOff, which is why this is its own
// helper rather than a call to Unlock.
func TestVerifyJoinKeyChecksAPassphraseOnAMachineThatIsNotSyncing(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	if on, err := Enabled(local); err != nil || on {
		t.Fatalf("the fixture machine is already syncing (%v, %v); Unlock would have been usable and this proves nothing", on, err)
	}
	transport := keyedLog(t, "the passphrase")

	ring, err := VerifyJoinKey(context.Background(), local, transport, []byte("the passphrase"))
	if err != nil {
		t.Fatalf("VerifyJoinKey: %v", err)
	}
	want := DeriveKey([]byte("the passphrase"), joinKeySalt, DefaultKDF)
	if !bytes.Equal(ring.Keys[ring.Epoch], want) {
		t.Error("VerifyJoinKey returned a key this passphrase does not derive from the remote's salt")
	}
	// It records what it learned, so status can report a locked machine without a
	// round trip.
	if got := keyFact(t, local); got != "1" {
		t.Errorf("%s = %q, want \"1\"", KeyRemoteKDF, got)
	}
}

// The wrong passphrase is refused and nothing is recorded: a joiner that guessed
// wrong must not leave behind a machine claiming the remote holds a key it never
// proved it could open.
func TestVerifyJoinKeyRefusesTheWrongPassphrase(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	transport := keyedLog(t, "the passphrase")
	if _, err := VerifyJoinKey(context.Background(), local, transport, []byte("not the passphrase")); err == nil {
		t.Fatal("VerifyJoinKey accepted the wrong passphrase")
	}
	if _, ok, err := local.KVGet(KeyRemoteKDF); err != nil || ok {
		t.Errorf("a refused join recorded %s (present %v, err %v)", KeyRemoteKDF, ok, err)
	}
	if key, ok, _ := ReadKey(local); ok {
		t.Errorf("a refused join stored a key: %q", key)
	}
}

// A remote holding no key is the one refusal a join cannot work around: the
// joiner would import every entry and hold none of them. The log this asks about
// is one nothing ever wrote key material to.
func TestVerifyJoinKeyRefusesARemoteWithNoKey(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	if _, err := VerifyJoinKey(context.Background(), local, synclog.NewMemTransport("a"), []byte("any passphrase")); err == nil {
		t.Fatal("VerifyJoinKey accepted a remote holding no key")
	}
}

// recordRemoteKeyFact is the writing half of RecordRemoteKDF, split out for the
// caller that has just asked the remote rather than asking it again.
//
// Both states are written: a remote that holds a key records "1" and one that
// does not records "0". Recording only the first would leave a machine claiming a
// key a remote has since lost.
func TestRecordRemoteKeyFactWritesBothStates(t *testing.T) {
	t.Parallel()
	_, _, local := openSplit(t)
	present, err := keyedLog(t, "p").KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
	if err := recordRemoteKeyFact(local, present); err != nil {
		t.Fatalf("recordRemoteKeyFact: %v", err)
	}
	if got := keyFact(t, local); got != "1" {
		t.Errorf("%s = %q, want \"1\"", KeyRemoteKDF, got)
	}
	absent, err := synclog.NewMemTransport("a").KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
	if err := recordRemoteKeyFact(local, absent); err != nil {
		t.Fatalf("recordRemoteKeyFact: %v", err)
	}
	if got := keyFact(t, local); got != "0" {
		t.Errorf("%s = %q, want \"0\"", KeyRemoteKDF, got)
	}
}

// joinKeySalt is the salt the keyed fixture derives from. It is fixed so the
// expected key is a value the test can state rather than one it asks the code
// under test to compute.
var joinKeySalt = []byte("0123456789abcdef")

// keyedLog is an in-memory remote holding key material for one passphrase.
//
// The log holds the key rather than the machine, because that is what a remote
// does: the machine sends a passphrase and the remote answers whether it matches.
// A fixture that stored the expected key locally would pass whatever the code did
// with the passphrase, which is the thing being tested.
func keyedLog(t *testing.T, passphrase string) *synclog.MemTransport {
	t.Helper()
	log := synclog.NewMemTransport("a")
	key := DeriveKey([]byte(passphrase), joinKeySalt, DefaultKDF)
	if _, err := log.KeyInit(context.Background(), synclog.KeyMeta{
		Salt: joinKeySalt, KDF: DefaultKDF.String(), Check: KeyCheck(key), Present: true,
	}); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	return log
}

func keyFact(t *testing.T, l Local) string {
	t.Helper()
	value, ok, err := l.KVGet(KeyRemoteKDF)
	if err != nil || !ok {
		t.Fatalf("read %s: present %v, err %v", KeyRemoteKDF, ok, err)
	}
	return string(bytes.TrimSpace(value))
}

// Staged R2 credentials and a join marker written after the snapshot go away on
// a machine that held none, and come back as they were on one that did.
func TestJoinSnapshotRestoresR2AndTheJoinMarker(t *testing.T) {
	t.Parallel()
	at := time.Unix(0, 0).UTC()
	staged := R2Secrets{Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "b", KeyID: "k", Secret: "s"}
	_, _, empty := openSplit(t)
	_, _, held := openSplit(t)
	if err := SetR2(held, R2Secrets{Endpoint: "https://old.r2.cloudflarestorage.com", Bucket: "ob", KeyID: "ok", Secret: "os"}, at); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	if err := WriteJoin(held, at); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	for name, local := range map[string]Local{"empty": empty, "held": held} {
		before := joinRows(t, local)
		snapshot, err := SnapshotJoin(local)
		if err != nil {
			t.Fatalf("%s: SnapshotJoin: %v", name, err)
		}
		if err := SetR2(local, staged, time.Now()); err != nil {
			t.Fatalf("%s: SetR2: %v", name, err)
		}
		if err := WriteJoin(local, time.Now()); err != nil {
			t.Fatalf("%s: WriteJoin: %v", name, err)
		}
		if err := snapshot.Restore(local, time.Now()); err != nil {
			t.Fatalf("%s: Restore: %v", name, err)
		}
		joinRowsEqual(t, name+" restore", before, joinRows(t, local))
	}
}
