//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// failReadOnlyWithLock makes the direct read-only config opener report the lock
// the way a second process's open does, without a real second process.
func failReadOnlyWithLock(t *testing.T) {
	t.Helper()
	orig := openReadOnlyDB
	openReadOnlyDB = func(string, db.Options) (*db.DB, error) {
		return nil, db.ErrLocked
	}
	t.Cleanup(func() { openReadOnlyDB = orig })
}

// configDirPathFor is <XDG_CONFIG_HOME>/relevo for the current test's env.
func configDirPathFor(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "relevo")
}

// TestPeekReadsConfigThroughTheOwnerWhenTheFileIsHeld pins the peek's on-locked
// policy: when the daemon holds the file, `--preflight`/`--check` read config
// through the owner instead of failing.
func TestPeekReadsConfigThroughTheOwnerWhenTheFileIsHeld(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	served := startTestOwner(t, root)

	// Seed a candidate into the machine-local file through the direct handle
	// the owner serves, so a config read that reaches the database sees a
	// non-default value. Config lives in the local file, so the shared file
	// would be a row no reader looks for.
	if err := served.db.Local().Tx(func(tx *db.Tx) error {
		return tx.ConfigPut("candidates", []byte(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`), time.Now().UTC())
	}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}
	failReadOnlyWithLock(t)

	rt, err := newRuntimePeek()
	if err != nil {
		t.Fatalf("newRuntimePeek: %v", err)
	}
	if got := rt.Candidates.Len(); got != 1 {
		t.Errorf("candidates = %d, want the one seeded row the owner served", got)
	}
	if got := atomic.LoadInt32(&served.ln.accepts); got == 0 {
		t.Errorf("the peek read no connection through the owner")
	}
}

// TestDaemonPreLockLoadSkipsAHeldFile pins the daemon's on-locked policy: a
// held file is skipped -- files or defaults -- with no dial and no error, so
// the start proceeds to the daemon lock.
func TestDaemonPreLockLoadSkipsAHeldFile(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	served := startTestOwner(t, root)
	failReadOnlyWithLock(t)

	if _, err := loadConfigReadOnly(machineRoot(t), configDirPathFor(t), lockedSkip); err != nil {
		t.Fatalf("loadConfigReadOnly(skip): %v, want the files or defaults", err)
	}
	if got := atomic.LoadInt32(&served.ln.accepts); got != 0 {
		t.Errorf("the pre-lock load dialled the owner %d times, want 0", got)
	}
}

// TestDaemonPreLockLoadSkipsAnUnconvertedFile pins the daemon's pre-lock policy
// for a database this build has not converted: like a held file, it is left
// alone -- files or defaults -- with no error, so the start reaches the writable
// open that converts and marks it.
func TestDaemonPreLockLoadSkipsAnUnconvertedFile(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	startTestOwner(t, root)

	orig := openReadOnlyDB
	openReadOnlyDB = func(string, db.Options) (*db.DB, error) {
		return nil, db.ErrNotConverted
	}
	t.Cleanup(func() { openReadOnlyDB = orig })

	if _, err := loadConfigReadOnly(machineRoot(t), configDirPathFor(t), lockedSkip); err != nil {
		t.Fatalf("loadConfigReadOnly(skip) with an unconverted file: %v, want the files or defaults", err)
	}
}

// TestBugreportReadsThroughTheOwnerWhenTheFileIsHeld pins the bundle's policy:
// when the daemon holds the file, the bundle's handle is a dialled one and its
// reads reach the owner.
func TestBugreportReadsThroughTheOwnerWhenTheFileIsHeld(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	served := startTestOwner(t, root)
	failReadOnlyWithLock(t)

	rt, _, err := newRuntimeReadOnly()
	if err != nil {
		t.Fatalf("newRuntimeReadOnly: %v", err)
	}
	if rt.DB == nil {
		t.Fatal("the bundle's handle is nil, want a dialled one")
	}
	defer func() { _ = rt.DB.Close() }()
	if got := rt.DB.Route(); !strings.HasPrefix(got, "owner ") {
		t.Errorf("route = %q, want a dialled handle", got)
	}
	if got := atomic.LoadInt32(&served.ln.accepts); got == 0 {
		t.Errorf("the bundle read no connection through the owner")
	}
}
