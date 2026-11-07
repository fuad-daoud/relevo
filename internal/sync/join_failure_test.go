package sync

import (
	"testing"
	"time"
)

// A written failure reads back with its stamp and message, and clearing it
// leaves no record behind.
func TestJoinFailureRoundTripThroughTheLocalFile(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if err := WriteJoinFailure(local, at, "sync: enable: import: the remote refused"); err != nil {
		t.Fatalf("WriteJoinFailure: %v", err)
	}
	failure, ok, err := ReadJoinFailure(local)
	if err != nil || !ok {
		t.Fatalf("ReadJoinFailure = (%+v, %v, %v), want the written failure", failure, ok, err)
	}
	if !failure.At.Equal(at) {
		t.Errorf("At = %v, want %v", failure.At, at)
	}
	if failure.Message != "sync: enable: import: the remote refused" {
		t.Errorf("Message = %q, want the written text", failure.Message)
	}
	if err := ClearJoinFailure(local); err != nil {
		t.Fatalf("ClearJoinFailure: %v", err)
	}
	if _, ok, err := ReadJoinFailure(local); err != nil || ok {
		t.Errorf("after clear = (ok %v, err %v), want no record", ok, err)
	}
}

// A failure body that will not parse is a failure rather than absent, so a
// stranded join never reads as a machine at rest.
func TestJoinFailureBodyThatWillNotParseReadsAsFailure(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	if err := local.KVPut(KeyJoinFailure, []byte(`{"at":"not-a-time"}`)); err != nil {
		t.Fatalf("plant a corrupt failure: %v", err)
	}
	if _, ok, err := ReadJoinFailure(local); err == nil {
		t.Errorf("ReadJoinFailure = (ok %v, nil), want the corrupt body to fail, never absent", ok)
	}
}

// The restart note fires only for a stranded marker: set on a machine that is
// off with no failure recorded. Every other shape writes nothing.
func TestNoteDaemonRestartWritesOnlyForAStrandedMarker(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		marked  bool
		enabled bool
		failed  bool
		want    bool
	}{
		{"a set marker on an off machine with no failure records the restart", true, false, false, true},
		{"no marker writes nothing", false, false, false, false},
		{"an enabled machine writes nothing", true, true, false, false},
		{"a failure already recorded writes nothing", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, local := openSplit(t)
			if tc.marked {
				if err := WriteJoin(local, now.Add(-time.Hour)); err != nil {
					t.Fatalf("WriteJoin: %v", err)
				}
			}
			if tc.enabled {
				if err := MarkEnabled(local, true, now.Add(-time.Hour)); err != nil {
					t.Fatalf("MarkEnabled: %v", err)
				}
			}
			if tc.failed {
				if err := WriteJoinFailure(local, now.Add(-time.Hour), "sync: enable: import: the remote refused"); err != nil {
					t.Fatalf("WriteJoinFailure: %v", err)
				}
			}
			wrote, err := NoteDaemonRestart(local, now)
			if err != nil {
				t.Fatalf("NoteDaemonRestart: %v", err)
			}
			if wrote != tc.want {
				t.Errorf("wrote = %v, want %v", wrote, tc.want)
			}
			_, ok, err := ReadJoinFailure(local)
			if err != nil {
				t.Fatalf("ReadJoinFailure: %v", err)
			}
			if ok != (tc.failed || tc.want) {
				t.Errorf("failure present = %v, want %v", ok, tc.failed || tc.want)
			}
		})
	}
}
