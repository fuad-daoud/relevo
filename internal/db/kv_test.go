package db

import (
	"errors"
	"reflect"
	"testing"
)

func TestKVRoundTrip(t *testing.T) {
	d := openTestDB(t)

	if _, ok, err := d.KVGet("ledger"); err != nil || ok {
		t.Fatalf("KVGet absent = (_, %v, %v), want (_, false, nil)", ok, err)
	}

	if err := d.KVPut("ledger", []byte(`{"entries":[]}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	got, ok, err := d.KVGet("ledger")
	if err != nil || !ok {
		t.Fatalf("KVGet = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	if string(got) != `{"entries":[]}` {
		t.Errorf("KVGet = %q, want {\"entries\":[]}", got)
	}

	if err := d.KVPut("ledger", []byte(`{"entries":[1]}`)); err != nil {
		t.Fatalf("KVPut overwrite: %v", err)
	}
	if got, _, _ := d.KVGet("ledger"); string(got) != `{"entries":[1]}` {
		t.Errorf("KVGet after overwrite = %q, want {\"entries\":[1]}", got)
	}

	if err := d.KVDelete("ledger"); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	if _, ok, err := d.KVGet("ledger"); err != nil || ok {
		t.Fatalf("KVGet after delete = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	// A delete of an absent key is not an error.
	if err := d.KVDelete("ledger"); err != nil {
		t.Fatalf("KVDelete absent: %v", err)
	}
}

func TestKVKeysSortedAndFiltered(t *testing.T) {
	d := openTestDB(t)
	for _, k := range []string{"serve.ui", "latency", "ui", "ledger"} {
		if err := d.KVPut(k, []byte(`{}`)); err != nil {
			t.Fatalf("KVPut %s: %v", k, err)
		}
	}

	all, err := d.KVKeys("")
	if err != nil {
		t.Fatalf("KVKeys(\"\"): %v", err)
	}
	want := []string{"latency", "ledger", "serve.ui", "ui"}
	if !reflect.DeepEqual(all, want) {
		t.Errorf("KVKeys(\"\") = %v, want %v", all, want)
	}

	serve, err := d.KVKeys("serve.")
	if err != nil {
		t.Fatalf("KVKeys(serve.): %v", err)
	}
	if !reflect.DeepEqual(serve, []string{"serve.ui"}) {
		t.Errorf("KVKeys(serve.) = %v, want [serve.ui]", serve)
	}

	if none, err := d.KVKeys("nope"); err != nil || len(none) != 0 {
		t.Errorf("KVKeys(nope) = (%v, %v), want ([], nil)", none, err)
	}
}

func TestKVPutRejectsInvalidJSON(t *testing.T) {
	d := openTestDB(t)
	if err := d.KVPut("ledger", []byte("{not json")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("KVPut invalid JSON err = %v, want ErrInvalid", err)
	}
	if _, ok, err := d.KVGet("ledger"); err != nil || ok {
		t.Fatalf("KVGet after refused put = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}
