//go:build !modernc

package db_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// replaceEnv replaces key in env rather than appending a second entry, which the
// child's os.Getenv would not read.
func replaceEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+val)
}

// runEngineChild runs the test binary as the engine helper against root, with
// HOME and the XDG directories pointed at home, so a library the child extracts
// can only be beside the database if the seam points the loader there.
func runEngineChild(t *testing.T, root, home string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	env := os.Environ()
	env = replaceEnv(env, engineHelperRootEnv, root)
	env = replaceEnv(env, "HOME", home)
	env = replaceEnv(env, "XDG_CACHE_HOME", filepath.Join(home, "cache"))
	env = replaceEnv(env, "XDG_STATE_HOME", filepath.Join(home, "state"))
	env = replaceEnv(env, "RELEVO_DBTEST_OWNER", "")

	cmd := exec.Command(self)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the engine helper failed: %v\n%s", err, out)
	}
}

// engineLibraryGlob matches the extracted Turso library under an engine cache
// directory, whatever hash the loader names its subdirectory.
const engineLibraryGlob = "turso-go/*/libturso_sync_sdk_kit.*"

// TestEngineLibraryIsExtractedOwnerOnlyBesideTheDatabase pins the cache seam:
// the child extracts the library beneath the database's directory, in an
// owner-only cache directory, and never into the user's cache.
func TestEngineLibraryIsExtractedOwnerOnlyBesideTheDatabase(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	runEngineChild(t, root, home)

	cacheDir := filepath.Join(root, "turso-go")
	info, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("the library cache directory %s: %v", cacheDir, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("cache directory mode = %o, want 700", perm)
	}

	libs, err := filepath.Glob(filepath.Join(root, engineLibraryGlob))
	if err != nil {
		t.Fatalf("glob the extracted library: %v", err)
	}
	if len(libs) == 0 {
		t.Fatalf("no extracted library under %s", cacheDir)
	}
	libInfo, err := os.Stat(libs[0])
	if err != nil {
		t.Fatalf("stat the extracted library: %v", err)
	}
	if libInfo.Size() == 0 {
		t.Errorf("the extracted library %s is empty", libs[0])
	}

	if _, err := os.Stat(filepath.Join(home, "turso-go")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the library was extracted into the user cache instead of beside the database: %v", err)
	}
}

// TestEngineReplacesACorruptCachedLibrary pins the mismatch retry: a cached
// library whose hash does not match is removed and re-extracted, so a corrupt
// cache heals instead of failing every later open.
func TestEngineReplacesACorruptCachedLibrary(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	runEngineChild(t, root, home)

	libs, err := filepath.Glob(filepath.Join(root, engineLibraryGlob))
	if err != nil || len(libs) == 0 {
		t.Fatalf("no extracted library to corrupt: %v (%v)", libs, err)
	}
	if err := os.WriteFile(libs[0], []byte("not a library"), 0o755); err != nil {
		t.Fatalf("corrupt the cached library: %v", err)
	}

	runEngineChild(t, root, home)

	info, err := os.Stat(libs[0])
	if err != nil {
		t.Fatalf("stat the repaired library: %v", err)
	}
	if info.Size() <= 1024 {
		t.Errorf("the cached library is still %d bytes, want a re-extracted copy", info.Size())
	}
}
