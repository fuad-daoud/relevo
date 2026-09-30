package db_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

// engineHelperRootEnv turns the test binary into the engine helper: the child
// opens (and so prepares the engine for) the database under the named root, then
// exits, so a test can inspect what the engine extracted in a fresh process.
const engineHelperRootEnv = "RELEVO_ENGINE_HELPER_ROOT"

// TestMain joins the transparency switch: when RELEVO_DBTEST_OWNER is set, every
// database this package opens is reached through an in-process owner, and the
// unset default opens the file directly. dbtest.Main is not used here, because
// this package migrates its own fixtures rather than seeding from the template.
// The engine-helper dispatch runs first, so a child process opens directly.
func TestMain(m *testing.M) {
	if root := os.Getenv(engineHelperRootEnv); root != "" {
		os.Exit(runEngineHelper(root))
	}
	cleanup, err := dbtest.OwnerMode()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// runEngineHelper opens the helper's database, which prepares the engine, and
// reports success through its exit code.
func runEngineHelper(root string) int {
	d, err := db.Open(filepath.Join(root, "relevo.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "engine helper open:", err)
		return 1
	}
	if err := d.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "engine helper close:", err)
		return 1
	}
	return 0
}
