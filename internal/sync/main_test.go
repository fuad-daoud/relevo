package sync

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain pins the driver's scratch directories for the whole package run.
//
// The engine records TURSO_TMPDIR and SQLITE_TMPDIR once per process, from the
// first database it opens. Under parallel tests that first opener is whichever
// test reaches it first, and its directory is a t.TempDir that vanishes when
// that test finishes -- so a later vacuum in a still-running test fails with
// "I/O error (tempdir): entity not found", depending only on scheduling. Seen
// as UploadThenEnable failing beside (not inside) any new test. Seeding both
// variables up front, to a directory nothing cleans, makes the scheduling
// irrelevant. The directory is deliberately not removed: the OS reaps it, and
// removing it at the end would reintroduce the same race for whatever runs
// last.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "relevo-sync-test-scratch-")
	if err != nil {
		panic("sync test scratch: " + err.Error())
	}
	dir = filepath.Join(dir, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		panic("sync test scratch: " + err.Error())
	}
	for _, env := range []string{"TURSO_TMPDIR", "SQLITE_TMPDIR"} {
		if os.Getenv(env) == "" {
			os.Setenv(env, dir)
		}
	}
	os.Exit(m.Run())
}
