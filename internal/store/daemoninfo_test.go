package store

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDaemonInfoRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	base := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	want := DaemonInfo{
		Version:    "v1.2.3",
		PID:        4242,
		StartedAt:  base,
		Exe:        "/usr/local/bin/relevo",
		ExeID:      FileID{Dev: 1, Ino: 2, Size: 3, ModTime: base},
		ReexecFrom: "v1.2.2",
		ReexecFailed: &ReexecFailure{
			ExeID:  FileID{Dev: 4, Ino: 5, Size: 6, ModTime: base.Add(time.Hour)},
			At:     base.Add(time.Minute),
			Reason: "policy.json: unknown field",
		},
	}

	if err := s.WriteDaemonInfo(want); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	got, ok, err := s.ReadDaemonInfo()
	if err != nil {
		t.Fatalf("ReadDaemonInfo: %v", err)
	}
	if !ok {
		t.Fatal("ReadDaemonInfo: ok = false, want true")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestWriteDaemonInfoPlainStart pins the write for a plain start: the kv
// upsert either lands or it does not (no temp file survives), the row omits an
// empty reexec_from and a null reexec_failed, and it reads back.
func TestWriteDaemonInfoPlainStart(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.WriteDaemonInfo(DaemonInfo{Version: "v1"}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp") {
			t.Errorf("temp file %q survived the write", e.Name())
		}
	}

	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	raw, ok, err := d.KVGet(daemonInfoKey)
	if err != nil || !ok {
		t.Fatalf("KVGet(%s) = (_, %v, %v), want the row", daemonInfoKey, ok, err)
	}
	for _, key := range []string{"reexec_from", "reexec_failed"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("daemon row contains %q for a plain start: %s", key, raw)
		}
	}
	if info, ok, err := s.ReadDaemonInfo(); err != nil || !ok || info.Version != "v1" {
		t.Errorf("ReadDaemonInfo after write = (%+v, %v, %v), want the record", info, ok, err)
	}
}

func TestReadDaemonInfoMissingIsNotAnError(t *testing.T) {
	s := New(t.TempDir())
	info, ok, err := s.ReadDaemonInfo()
	if err != nil {
		t.Fatalf("ReadDaemonInfo on a missing record: %v", err)
	}
	if ok {
		t.Error("ok = true for a missing record, want false")
	}
	if !reflect.DeepEqual(info, DaemonInfo{}) {
		t.Errorf("info = %+v, want the zero value", info)
	}
}

// TestReadDaemonInfoMalformedIsAnError pins that a row that is not a daemon
// record is an error, not a silent (zero, false, nil).
func TestReadDaemonInfoMalformedIsAnError(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := d.KVPut(daemonInfoKey, []byte(`{"pid":"not-a-number"}`)); err != nil {
		t.Fatalf("seed malformed daemon row: %v", err)
	}
	if _, _, err := s.ReadDaemonInfo(); err == nil {
		t.Fatal("ReadDaemonInfo on a malformed row: err = nil, want an error")
	}
}

func TestRemoveDaemonInfo(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.WriteDaemonInfo(DaemonInfo{Version: "v1"}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	if err := s.RemoveDaemonInfo(); err != nil {
		t.Fatalf("RemoveDaemonInfo: %v", err)
	}
	if _, ok, err := s.ReadDaemonInfo(); err != nil || ok {
		t.Fatalf("after RemoveDaemonInfo: ok=%v err=%v, want false, nil", ok, err)
	}
	// Removing again is a no-op: the clean-shutdown path must never fail on a
	// record already gone.
	if err := s.RemoveDaemonInfo(); err != nil {
		t.Fatalf("RemoveDaemonInfo on a missing record: %v", err)
	}
}
