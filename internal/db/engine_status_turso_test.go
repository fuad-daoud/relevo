//go:build !modernc

package db

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// engineHelperRootEnv is main_test.go's dispatch variable, repeated here because
// an internal test cannot read the external test package's constant: the test
// binary extracts the engine's library under the named root and exits.
const engineHelperRootEnv = "RELEVO_ENGINE_HELPER_ROOT"

// extractEngineInChild makes a second process open a database under root, which
// extracts the Turso library there, and exits. The library must be extracted in
// a child: the test then corrupts the file, and a corrupting write to a library
// this process had already loaded would fault at exit.
func extractEngineInChild(t *testing.T, root string) {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(envWithout(os.Environ(), "RELEVO_DBTEST_OWNER"), engineHelperRootEnv+"="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the extraction helper failed: %v\n%s", err, out)
	}
}

// TestEngineStatusReportsACorruptCachedLibrary pins the mismatch path: a
// present library whose hash no longer matches is an error, and EngineStatus
// never removes or retries it -- clearing the cache is the doctor row's stated
// fix, not something doctor does.
func TestEngineStatusReportsACorruptCachedLibrary(t *testing.T) {
	root := t.TempDir()
	extractEngineInChild(t, root)

	matches, err := filepath.Glob(filepath.Join(root, "turso-go", "*", libraryFileName()))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no extracted library under %s: matches=%v err=%v", root, matches, err)
	}
	lib := matches[0]
	if err := os.WriteFile(lib, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("corrupt the cached library: %v", err)
	}

	status := EngineStatus(root)
	if status.Err == nil {
		t.Fatalf("EngineStatus on a corrupt cached library = %+v, want an error", status)
	}
	if status.Missing {
		t.Errorf("a present-but-corrupt library was reported missing: %+v", status)
	}
	if status.Library != lib {
		t.Errorf("library = %q, want %q", status.Library, lib)
	}
	if _, err := os.Stat(lib); err != nil {
		t.Errorf("EngineStatus removed or replaced the corrupt library: %v", err)
	}
}
