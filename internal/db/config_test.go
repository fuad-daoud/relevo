package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigRoundTrip(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()

	if _, ok, err := d.ConfigGet("policy"); err != nil || ok {
		t.Fatalf("ConfigGet absent = (_, %v, %v), want (_, false, nil)", ok, err)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.ConfigPut("policy", []byte(`{"a":1}`), now) }); err != nil {
		t.Fatalf("ConfigPut: %v", err)
	}
	body, ok, err := d.ConfigGet("policy")
	if err != nil || !ok {
		t.Fatalf("ConfigGet = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	if string(body) != `{"a":1}` {
		t.Errorf("body = %q, want {\"a\":1}", body)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.ConfigPut("policy", []byte(`{"b":2}`), now) }); err != nil {
		t.Fatalf("ConfigPut overwrite: %v", err)
	}
	if body, _, _ := d.ConfigGet("policy"); string(body) != `{"b":2}` {
		t.Errorf("body after overwrite = %q, want {\"b\":2}", body)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.ConfigDelete("policy") }); err != nil {
		t.Fatalf("ConfigDelete: %v", err)
	}
	if _, ok, err := d.ConfigGet("policy"); err != nil || ok {
		t.Fatalf("ConfigGet after delete = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}

func TestConfigVersionBumps(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()

	v0, err := d.ConfigVersion()
	if err != nil {
		t.Fatalf("ConfigVersion: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := d.Tx(func(tx *Tx) error { return tx.ConfigPut("policy", []byte(`{}`), now) }); err != nil {
			t.Fatalf("ConfigPut: %v", err)
		}
		v, err := d.ConfigVersion()
		if err != nil {
			t.Fatalf("ConfigVersion: %v", err)
		}
		if v != v0+int64(i+1) {
			t.Fatalf("version after %d puts = %d, want %d", i+1, v, v0+int64(i+1))
		}
	}

	if err := d.Tx(func(tx *Tx) error { return tx.ConfigDelete("policy") }); err != nil {
		t.Fatalf("ConfigDelete: %v", err)
	}
	v, err := d.ConfigVersion()
	if err != nil {
		t.Fatalf("ConfigVersion: %v", err)
	}
	if v != v0+3 {
		t.Fatalf("version after delete = %d, want %d", v, v0+3)
	}
}

func TestSecretRoundTrip(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()

	if _, ok, err := d.SecretGet("client.key"); err != nil || ok {
		t.Fatalf("SecretGet absent = (_, %v, %v), want (_, false, nil)", ok, err)
	}

	value := []byte{0x00, 0xff, 0x10}
	if err := d.Tx(func(tx *Tx) error { return tx.SecretPut("client.key", value, now) }); err != nil {
		t.Fatalf("SecretPut: %v", err)
	}
	got, ok, err := d.SecretGet("client.key")
	if err != nil || !ok {
		t.Fatalf("SecretGet = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	if string(got) != string(value) {
		t.Errorf("SecretGet = %v, want %v", got, value)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.SecretPut("typesafe", []byte("k"), now) }); err != nil {
		t.Fatalf("SecretPut typesafe: %v", err)
	}
	names, err := d.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames: %v", err)
	}
	if len(names) != 2 || names[0] != "client.key" || names[1] != "typesafe" {
		t.Errorf("SecretNames = %v, want [client.key typesafe]", names)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.SecretDelete("client.key") }); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}
	if _, ok, err := d.SecretGet("client.key"); err != nil || ok {
		t.Fatalf("SecretGet after delete = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}

func TestConfigImportRecord(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()

	if err := d.Tx(func(tx *Tx) error {
		return tx.ConfigImportRecord("candidates.json", "/tmp/candidates.json", []byte(`[]`), now)
	}); err != nil {
		t.Fatalf("ConfigImportRecord: %v", err)
	}

	var name, path string
	var body []byte
	if err := d.sqlDB.QueryRow(`SELECT name, source_path, body FROM config_import`).Scan(&name, &path, &body); err != nil {
		t.Fatalf("select config_import: %v", err)
	}
	if name != "candidates.json" || path != "/tmp/candidates.json" || string(body) != `[]` {
		t.Errorf("config_import = (%q, %q, %q)", name, path, body)
	}
}

func TestOpenReadOnlyMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	_, err := OpenReadOnly(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly missing file err = %v, want wrapping os.ErrNotExist", err)
	}
}

func TestOpenChmodsDBFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o, want 0600", info.Mode().Perm())
	}
}
