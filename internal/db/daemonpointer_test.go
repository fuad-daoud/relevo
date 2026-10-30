package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDaemonPointerKVRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("open test machine db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	startedAt := time.Date(2026, 9, 20, 12, 34, 56, 0, time.UTC)
	want := DaemonPointer{
		Root:      "/srv/data/relevo-serve/serve",
		PID:       4242,
		Listen:    ":7777",
		StartedAt: startedAt,
	}
	if err := WriteDaemonPointer(d, want); err != nil {
		t.Fatalf("WriteDaemonPointer: %v", err)
	}
	got, ok, err := ReadDaemonPointer(d)
	if err != nil {
		t.Fatalf("ReadDaemonPointer: %v", err)
	}
	if !ok {
		t.Fatal("ReadDaemonPointer ok = false, want true")
	}
	if got != want {
		t.Errorf("ReadDaemonPointer = %+v, want %+v", got, want)
	}
	if err := RemoveDaemonPointer(d); err != nil {
		t.Fatalf("RemoveDaemonPointer: %v", err)
	}
	_, ok, err = ReadDaemonPointer(d)
	if err != nil {
		t.Fatalf("ReadDaemonPointer after remove: %v", err)
	}
	if ok {
		t.Error("ReadDaemonPointer after remove ok = true, want false")
	}
}
