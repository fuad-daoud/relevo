package store

import (
	"os"
	"testing"
	"time"
)

// TestWALHealFailureRoundTrip pins the exhaustion marker's shape: a write
// reads back with every field intact, and clearing it reads back absent.
func TestWALHealFailureRoundTrip(t *testing.T) {
	s := New(t.TempDir())

	if _, ok, err := s.ReadWALHealFailure(); err != nil || ok {
		t.Fatalf("ReadWALHealFailure before any write = (ok=%v, err=%v), want absent", ok, err)
	}

	at := time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)
	if err := s.WriteWALHealFailure(3, at, "attempts exhausted"); err != nil {
		t.Fatalf("WriteWALHealFailure: %v", err)
	}
	got, ok, err := s.ReadWALHealFailure()
	if err != nil || !ok {
		t.Fatalf("ReadWALHealFailure after write = (ok=%v, err=%v), want present", ok, err)
	}
	if got.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", got.Attempts)
	}
	if !got.At.Equal(at) {
		t.Errorf("At = %v, want %v", got.At, at)
	}
	if got.Reason != "attempts exhausted" {
		t.Errorf("Reason = %q, want the exhaustion reason", got.Reason)
	}

	if err := s.ClearWALHealFailure(); err != nil {
		t.Fatalf("ClearWALHealFailure: %v", err)
	}
	if _, ok, err := s.ReadWALHealFailure(); err != nil || ok {
		t.Fatalf("ReadWALHealFailure after clear = (ok=%v, err=%v), want absent", ok, err)
	}

	// Clearing a marker that was never written is nil: a heal that worked
	// clears unconditionally.
	if err := s.ClearWALHealFailure(); err != nil {
		t.Fatalf("second ClearWALHealFailure = %v, want nil", err)
	}
}

// TestWALHealFailureMalformedReadsAsError pins the unreadable-marker branch:
// garbage in the marker file reads as an error with no failure, never as a
// present zero failure a reader could mistake for a real give-up.
func TestWALHealFailureMalformedReadsAsError(t *testing.T) {
	s := New(t.TempDir())

	if err := os.WriteFile(s.walHealFailurePath(), []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}
	got, ok, err := s.ReadWALHealFailure()
	if err == nil {
		t.Fatal("ReadWALHealFailure on garbage = nil error, want a decode error")
	}
	if ok {
		t.Fatal("ReadWALHealFailure on garbage = present, want absent")
	}
	if got.Attempts != 0 || got.Reason != "" || !got.At.IsZero() {
		t.Fatalf("ReadWALHealFailure on garbage = %+v, want the zero failure", got)
	}
}
