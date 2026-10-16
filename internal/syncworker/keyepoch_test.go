package syncworker

import (
	"errors"
	"testing"
)

// TestKeyMetaReadsAnAbsentEpochRowAsEpochOne pins the default: a remote that
// has never rekeyed holds no key_epoch row at all, and every reader is handed
// epoch 1 rather than a zero each caller would have to default itself.
func TestKeyMetaReadsAnAbsentEpochRowAsEpochOne(t *testing.T) {
	t.Parallel()
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")

	key, err := b.KeyMeta()
	if err != nil {
		t.Fatalf("KeyMeta on a fresh remote: %v", err)
	}
	if key.Epoch != 1 {
		t.Errorf("epoch = %d, want 1: an absent row is epoch 1", key.Epoch)
	}
	if key.Wrapped != nil {
		t.Errorf("wrapped = %v, want none: nothing has been rekeyed", key.Wrapped)
	}
}

// TestKeyMetaReadsTheEpochRowsARekeyWrote pins the read of the two rows a
// rekey writes: the epoch the material's key is, and each older epoch's key
// sealed under it.
func TestKeyMetaReadsTheEpochRowsARekeyWrote(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path,
		"INSERT INTO meta (key, value) VALUES ('kdf_salt', '"+encodeKeyValue([]byte("the new salt"))+"')",
		"INSERT INTO meta (key, value) VALUES ('key_epoch', '3')",
		"INSERT INTO meta (key, value) VALUES ('key_wrapped.1', '"+encodeKeyValue([]byte("epoch one's key"))+"')",
		"INSERT INTO meta (key, value) VALUES ('key_wrapped.2', '"+encodeKeyValue([]byte("epoch two's key"))+"')")

	key, err := b.KeyMeta()
	if err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
	if key.Epoch != 3 {
		t.Errorf("epoch = %d, want 3", key.Epoch)
	}
	if string(key.Wrapped[1]) != "epoch one's key" || string(key.Wrapped[2]) != "epoch two's key" {
		t.Errorf("wrapped = %v, want both older epochs' keys", key.Wrapped)
	}
}

// TestKeyMetaRefusesAMalformedKeyEpochRow pins that a key_epoch row the remote
// wrote badly is a refusal rather than a read of epoch 1: the remote is
// untrusted, and a row this build cannot read must not quietly mean "never
// rekeyed".
func TestKeyMetaRefusesAMalformedKeyEpochRow(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"1", "02", "x", "-3", "", "2 "} {
		path := tempReplica(t)
		b := backendFor(t, &localDriver{path: path}, "origin-a")
		execOn(t, path, "INSERT INTO meta (key, value) VALUES ('key_epoch', '"+value+"')")

		if _, err := b.KeyMeta(); !errors.Is(err, ErrKeyEpoch) {
			t.Errorf("key_epoch %q = %v, want ErrKeyEpoch", value, err)
		}
	}
}

// TestKeyMetaRefusesAMalformedKeyWrappedRow pins the same rule for the wrapped
// rows' names: the epoch a row claims is the remote's claim, and one this
// build cannot read is refused rather than skipped.
func TestKeyMetaRefusesAMalformedKeyWrappedRow(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"key_wrapped.0", "key_wrapped.01", "key_wrapped.x"} {
		path := tempReplica(t)
		b := backendFor(t, &localDriver{path: path}, "origin-a")
		execOn(t, path,
			"INSERT INTO meta (key, value) VALUES ('"+name+"', '"+encodeKeyValue([]byte("a key"))+"')")

		if _, err := b.KeyMeta(); !errors.Is(err, ErrKeyEpoch) {
			t.Errorf("%s = %v, want ErrKeyEpoch", name, err)
		}
	}
}
