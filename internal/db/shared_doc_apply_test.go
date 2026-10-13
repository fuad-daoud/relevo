package db

import (
	"reflect"
	"testing"
	"time"
)

func TestDocAppliedReadsBackTheNewestWrite(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error {
		if _, found, err := tx.DocApplied("config", "policy"); err != nil || found {
			t.Errorf("DocApplied before any write = found %t, err %v; want absent", found, err)
		}
		if err := tx.SetDocApplied("config", "policy", "m1", "5"); err != nil {
			return err
		}
		if err := tx.SetDocApplied("config", "policy", "m2", "9"); err != nil {
			return err
		}
		got, found, err := tx.DocApplied("config", "policy")
		if want := (DocApplied{Origin: "m2", Stamp: "9"}); err != nil || !found || got != want {
			t.Errorf("DocApplied = %+v, %t, %v; want %+v", got, found, err, want)
		}
		if _, found, err := tx.DocApplied("config", "roles"); err != nil || found {
			t.Errorf("DocApplied of another key = found %t, err %v; want absent", found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func TestExportedStateIsSetAndCleared(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error { return tx.SetDocExported("config", "policy", "m2", "9", "digest") }); err != nil {
		t.Fatalf("SetDocExported: %v", err)
	}
	state, err := d.DocExportState("config")
	if want := map[string]DocExportRow{"policy": {Origin: "m2", Stamp: "9", Digest: "digest"}}; err != nil || !reflect.DeepEqual(state, want) {
		t.Fatalf("DocExportState = %v, %v; want %v", state, err, want)
	}
	if other, err := d.DocExportState("other"); err != nil || len(other) != 0 {
		t.Errorf("DocExportState of another doc = %v, %v; want none", other, err)
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ClearDocExported("config", "policy") }); err != nil {
		t.Fatalf("ClearDocExported: %v", err)
	}
	if state, err := d.DocExportState("config"); err != nil || len(state) != 0 {
		t.Errorf("DocExportState after the clear = %v, %v; want none", state, err)
	}
}

func TestParkedDocsComeBackInArrivalOrderUntilUnparked(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error {
		if err := tx.ParkDoc("m2", 1, "second", "2026-10-12T10:00:00Z"); err != nil {
			return err
		}
		if err := tx.ParkDoc("m1", 4, "first", "2026-10-12T09:00:00Z"); err != nil {
			return err
		}
		return tx.ParkDoc("m2", 1, "second again", "2026-10-12T10:00:00Z")
	}); err != nil {
		t.Fatalf("park: %v", err)
	}
	got, err := d.PendingDocs()
	want := []PendingDoc{{Origin: "m1", Seq: 4, Entry: "first"}, {Origin: "m2", Seq: 1, Entry: "second again"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingDocs = %v, %v; want %v", got, err, want)
	}
	if err := d.Tx(func(tx *Tx) error { return tx.UnparkDoc("m1", 4) }); err != nil {
		t.Fatalf("UnparkDoc: %v", err)
	}
	if got, err := d.PendingDocs(); err != nil || len(got) != 1 || got[0].Origin != "m2" {
		t.Errorf("PendingDocs after unparking m1 = %v, %v; want only m2", got, err)
	}
}

func TestRecordDocExportWritesStateAndAppliedAndKeepsTombstonesApplied(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	putConfig(t, d, `{"a":1}`)
	sigs, err := d.DocSignals()
	if err != nil || len(sigs) != 1 {
		t.Fatalf("DocSignals = %v, %v", sigs, err)
	}
	if err := d.Tx(func(tx *Tx) error { return tx.SetDocExported("config", "gone", "m1", "1", "old") }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	writes := []DocExportWrite{
		{Key: "policy", Origin: "m1", Stamp: "7", Digest: "d7"},
		{Key: "gone", Origin: "m1", Stamp: "8", Tombstone: true},
	}
	if err := d.RecordDocExport(sigs[0], writes); err != nil {
		t.Fatalf("RecordDocExport: %v", err)
	}

	state, err := d.DocExportState("config")
	if want := map[string]DocExportRow{"policy": {Origin: "m1", Stamp: "7", Digest: "d7"}}; err != nil || !reflect.DeepEqual(state, want) {
		t.Errorf("export state = %v, %v; want only the upserted key %v", state, err, want)
	}
	if err := d.Tx(func(tx *Tx) error {
		for key, want := range map[string]DocApplied{"policy": {Origin: "m1", Stamp: "7"}, "gone": {Origin: "m1", Stamp: "8"}} {
			got, found, err := tx.DocApplied("config", key)
			if err != nil || !found || got != want {
				t.Errorf("DocApplied(%s) = %+v, %t, %v; want %+v", key, got, found, err, want)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
	if after, err := d.DocSignals(); err != nil || len(after) != 0 {
		t.Errorf("signals after the export = %v, %v; want none", after, err)
	}
}

func TestClearDocExportStateSignalsEachDocThatHadState(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error {
		if err := tx.SetDocExported("config", "policy", "m1", "1", "d"); err != nil {
			return err
		}
		return tx.SetDocExported("config", "roles", "m1", "2", "d")
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.ClearDocExportState(); err != nil {
		t.Fatalf("ClearDocExportState: %v", err)
	}
	if state, err := d.DocExportState("config"); err != nil || len(state) != 0 {
		t.Errorf("export state = %v, %v; want none", state, err)
	}
	if sigs, err := d.DocSignals(); err != nil || len(sigs) != 1 || sigs[0].Doc != "config" {
		t.Errorf("signals = %v, %v; want the one config signal so the next export re-emits", sigs, err)
	}
}

func TestConfigUpdatedAtReportsWhenASectionWasWritten(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if _, ok, err := d.ConfigUpdatedAt("policy"); err != nil || ok {
		t.Fatalf("ConfigUpdatedAt of an absent section = %t, %v; want absent", ok, err)
	}
	at := time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)
	if err := d.Tx(func(tx *Tx) error { return tx.ConfigPut("policy", []byte(`{}`), at) }); err != nil {
		t.Fatalf("ConfigPut: %v", err)
	}
	got, ok, err := d.ConfigUpdatedAt("policy")
	if err != nil || !ok || !got.Equal(at) {
		t.Errorf("ConfigUpdatedAt = %v, %t, %v; want %v", got, ok, err, at)
	}
}

func TestConfigLocalKeysInsideATransactionSeeItsOwnWrites(t *testing.T) {
	t.Parallel()
	d := directOpenTestDB(t)
	t.Cleanup(func() { _ = d.Close() })
	now := time.Now().UTC()
	if err := d.Tx(func(tx *Tx) error {
		if err := tx.ConfigLocalPut("accounts/work", []byte(`{}`), now); err != nil {
			return err
		}
		if err := tx.ConfigLocalPut("policy/x", []byte(`{}`), now); err != nil {
			return err
		}
		keys, err := tx.ConfigLocalKeys("accounts/")
		if want := []string{"accounts/work"}; err != nil || !reflect.DeepEqual(keys, want) {
			t.Errorf("ConfigLocalKeys = %v, %v; want %v", keys, err, want)
		}
		return tx.ConfigLocalPut("accounts/work", []byte(`{"a":1}`), now)
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
	if got, ok, err := d.ConfigLocalGet("accounts/work"); err != nil || !ok || string(got) != `{"a":1}` {
		t.Errorf("ConfigLocalGet = %q, %t, %v; want the replaced patch", got, ok, err)
	}
}
