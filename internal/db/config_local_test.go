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

var errUnstoredOverride = &overrideError{}

type overrideError struct{}

func (*overrideError) Error() string { return "that key already has an override" }
