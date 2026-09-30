package bugreport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLastErrorRoundTrip pins the slot's whole contract: what one write leaves
// on disk -- its mode, its owner-only root, and no temp file beside it -- and
// that the next failure replaces the record rather than joining it.
func TestLastErrorRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	first := LastError{
		Time:    fixedNow.Add(-2 * time.Minute),
		Version: "0.4.2",
		Verb:    "send",
		Argv:    []string{"relevo", "send", "--name", "alpha"},
		Code:    "internal",
		Message: "write /home/fuad/.local/state/relevo/alpha/log: boom",
		Next:    "relevo bugreport",
	}
	if err := WriteLastError(root, first); err != nil {
		t.Fatalf("WriteLastError: %v", err)
	}

	got, ok, err := ReadLastError(root)
	if err != nil || !ok {
		t.Fatalf("ReadLastError = %+v, %v, %v", got, ok, err)
	}
	if got.Code != first.Code || got.Message != first.Message || len(got.Argv) != len(first.Argv) || !got.Time.Equal(first.Time) {
		t.Errorf("read %+v, wrote %+v", got, first)
	}

	info, err := os.Stat(filepath.Join(root, LastErrorFile))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 600", LastErrorFile, perm)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if perm := rootInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("state root mode = %o, want 700", perm)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("root holds %d files, want only %s", len(entries), LastErrorFile)
	}

	second := first
	second.Code = "internal"
	second.Message = "a later failure"
	if err := WriteLastError(root, second); err != nil {
		t.Fatalf("second WriteLastError: %v", err)
	}
	got, ok, err = ReadLastError(root)
	if err != nil || !ok {
		t.Fatalf("second ReadLastError = %+v, %v, %v", got, ok, err)
	}
	if got.Message != "a later failure" {
		t.Errorf("the slot holds %q, want the newest failure", got.Message)
	}
}

// TestReadLastErrorWithoutSlot pins the ordinary case: a machine that has never
// failed has no slot, and reading one is not an error.
func TestReadLastErrorWithoutSlot(t *testing.T) {
	got, ok, err := ReadLastError(filepath.Join(t.TempDir(), "nothing"))
	if err != nil || ok || got.Code != "" {
		t.Errorf("ReadLastError = %+v, %v, %v, want the zero record and no error", got, ok, err)
	}
}

// TestWriteLastErrorRefusesAnEmptyRoot pins the one input that must not become
// a write somewhere unexpected.
func TestWriteLastErrorRefusesAnEmptyRoot(t *testing.T) {
	if err := WriteLastError("", LastError{Code: "internal"}); err == nil {
		t.Error("WriteLastError with no root must fail")
	}
}
