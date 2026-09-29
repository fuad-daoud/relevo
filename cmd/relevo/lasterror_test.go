package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/bugreport"
	"github.com/fuad-daoud/relevo/internal/store"
)

// lastErrorSlot resolves the record the loop writes for a test's environment.
func lastErrorSlot(t *testing.T) string {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	return filepath.Join(root, bugreport.LastErrorFile)
}

// TestRecordInternalWritesSlot pins the record: one coded internal failure
// leaves its facts in the slot, owner-only, and the next one replaces it
// rather than appending.
func TestRecordInternalWritesSlot(t *testing.T) {
	docsEnv(t)

	recordInternal([]string{"status", "--json"}, fail(codeInternal, "boom"))

	slot := lastErrorSlot(t)
	fi, err := os.Stat(slot)
	if err != nil {
		t.Fatalf("stat slot: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("slot mode = %v, want 0600", fi.Mode().Perm())
	}

	root := filepath.Dir(slot)
	e, ok, err := bugreport.ReadLastError(root)
	if err != nil || !ok {
		t.Fatalf("ReadLastError: %v (ok=%v)", err, ok)
	}
	if e.Verb != "status" || e.Code != "internal" || e.Message != "boom" {
		t.Errorf("record = %+v, want verb status, code internal, message boom", e)
	}
	if len(e.Argv) != 2 || e.Argv[0] != "status" || e.Argv[1] != "--json" {
		t.Errorf("argv = %v, want the run's own args", e.Argv)
	}
	if e.Next != "relevo bugreport" {
		t.Errorf("next = %q, want the bundle command", e.Next)
	}
	if e.Version == "" {
		t.Error("record carries no version")
	}

	recordInternal([]string{"doctor"}, fail(codeInternal, "second"))
	e, ok, err = bugreport.ReadLastError(root)
	if err != nil || !ok {
		t.Fatalf("ReadLastError after rewrite: %v (ok=%v)", err, ok)
	}
	if e.Verb != "doctor" || e.Message != "second" {
		t.Errorf("record = %+v, want the newest failure only", e)
	}
}

// TestRecordSkipsNonInternal pins the code filter: a usage refusal and a
// missing binding are not internal, so neither leaves a record.
func TestRecordSkipsNonInternal(t *testing.T) {
	docsEnv(t)

	for _, err := range []error{
		fail(codeUsage, "boom"),
		fail(codeBindingNotFound, "boom"),
	} {
		recordInternal([]string{"status"}, err)
		if _, statErr := os.Stat(lastErrorSlot(t)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%v: recorded a failure that is not internal (stat err = %v)", err, statErr)
		}
	}
}

// TestRecordSkipsBugreport pins the self-exclusion: a bugreport run never
// records, so a bundle cannot record itself.
func TestRecordSkipsBugreport(t *testing.T) {
	docsEnv(t)

	recordInternal([]string{"bugreport", "--stdout"}, fail(codeInternal, "boom"))

	if _, statErr := os.Stat(lastErrorSlot(t)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a bugreport run recorded itself (stat err = %v)", statErr)
	}
}

// TestRecordFailureLeavesReportUnchanged pins the swallow: a state root that
// cannot be created leaves report's bytes and exit byte-identical with and
// without the recording attempt.
func TestRecordFailureLeavesReportUnchanged(t *testing.T) {
	// A state root below a regular file cannot be created, so the record can
	// never be written.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", blocker)

	errIn := fail(codeInternal, "boom")
	recordInternal([]string{"status"}, errIn)

	var withRecord bytes.Buffer
	exitWith := report(&withRecord, errIn, false)

	var without bytes.Buffer
	exitWithout := report(&without, fail(codeInternal, "boom"), false)

	if exitWith != exitWithout {
		t.Errorf("exit = %d with recording, %d without", exitWith, exitWithout)
	}
	if withRecord.String() != without.String() {
		t.Errorf("report wrote %q with recording, %q without", withRecord.String(), without.String())
	}
}
