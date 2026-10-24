package db

import (
	"reflect"
	"testing"
	"time"
)

// The machine-local config layer: one row per key, keyed so a caller can read
// every override of one section without reading the others.

func TestConfigLocalRoundTripsAPatchPerKey(t *testing.T) {
	t.Parallel()

	d := directOpenTestDB(t)
	t.Cleanup(func() { _ = d.Close() })
	now := time.Now().UTC()

	const patch = `{"config_dir":"/tmp/x"}`
	if err := d.Tx(func(tx *Tx) error {
		if _, ok, err := tx.ConfigLocalGet("accounts/work"); err != nil {
			return err
		} else if ok {
			return errUnstoredOverride
		}
		if err := tx.ConfigLocalPut("accounts/work", []byte(patch), now); err != nil {
			return err
		}
		if err := tx.ConfigLocalPut("accounts/side", []byte(`{}`), now); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed the local layer: %v", err)
	}

	got, ok, err := d.ConfigLocalGet("accounts/work")
	if err != nil || !ok {
		t.Fatalf("ConfigLocalGet = %q, %t, %v; want the stored patch", got, ok, err)
	}
	if string(got) != patch {
		t.Errorf("ConfigLocalGet = %q, want %q", got, patch)
	}

	// The prefix is what lets a section read its own overrides back without
	// seeing another's, so it has to match on a separator and not on a name
	// that merely starts the same way.
	keys, err := d.ConfigLocalKeys("accounts/")
	if err != nil {
		t.Fatalf("ConfigLocalKeys: %v", err)
	}
	if want := []string{"accounts/side", "accounts/work"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("ConfigLocalKeys = %v, want %v", keys, want)
	}
	if keys, err := d.ConfigLocalKeys("polic"); err != nil || len(keys) != 0 {
		t.Errorf("ConfigLocalKeys(polic) = %v, %v; want none", keys, err)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.ConfigLocalDelete("accounts/work") }); err != nil {
		t.Fatalf("ConfigLocalDelete: %v", err)
	}
	if _, ok, err := d.ConfigLocalGet("accounts/work"); err != nil || ok {
		t.Errorf("ConfigLocalGet after delete = present %t, err %v; want absent", ok, err)
	}
}

// TestConfigLocalWritesBumpConfigVersion pins the reload gate's contract at the
// row level: every write to the machine-local layer moves config_meta.version,
// the one counter both refreshers compare per tick, so the next Refresh sees a
// --local change; reads of the same layer leave the counter where it stands, so
// an unchanged tick stays one cheap query.
func TestConfigLocalWritesBumpConfigVersion(t *testing.T) {
	t.Parallel()

	d := directOpenTestDB(t)
	t.Cleanup(func() { _ = d.Close() })
	now := time.Now().UTC()

	version := func() int64 {
		v, err := d.ConfigVersion()
		if err != nil {
			t.Fatalf("ConfigVersion: %v", err)
		}
		return v
	}
	v0 := version()

	if err := d.Tx(func(tx *Tx) error {
		return tx.ConfigLocalPut("accounts/work", []byte(`{"config_dir":"/tmp/x"}`), now)
	}); err != nil {
		t.Fatalf("ConfigLocalPut: %v", err)
	}
	v1 := version()
	if v1 <= v0 {
		t.Errorf("version after ConfigLocalPut = %d, want > %d: a local write must move the reload counter", v1, v0)
	}

	// Reading the local layer is not writing it: the counter stands, which is
	// what lets a quiet tick skip the full config load.
	if patch, ok, err := d.ConfigLocalGet("accounts/work"); err != nil || !ok || len(patch) == 0 {
		t.Fatalf("ConfigLocalGet = %q, %t, %v; want the stored patch", patch, ok, err)
	}
	if keys, err := d.ConfigLocalKeys("accounts/"); err != nil || len(keys) != 1 {
		t.Fatalf("ConfigLocalKeys = %v, %v; want the one stored key", keys, err)
	}
	if v := version(); v != v1 {
		t.Errorf("version after no-op reads = %d, want %d unchanged", v, v1)
	}

	// A delete of a stored key moves it like any other local write.
	if err := d.Tx(func(tx *Tx) error { return tx.ConfigLocalDelete("accounts/work") }); err != nil {
		t.Fatalf("ConfigLocalDelete: %v", err)
	}
	v2 := version()
	if v2 <= v1 {
		t.Errorf("version after ConfigLocalDelete = %d, want > %d", v2, v1)
	}
}

var errUnstoredOverride = &overrideError{}

type overrideError struct{}

func (*overrideError) Error() string { return "that key already has an override" }
