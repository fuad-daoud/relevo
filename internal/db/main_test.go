package db_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

// engineHelperRootEnv turns the test binary into the engine helper: the child
// opens (and so prepares the engine for) the database under the named root, then
// exits, so a test can inspect what the engine extracted in a fresh process.
const engineHelperRootEnv = "RELEVO_ENGINE_HELPER_ROOT"

// The lock-helper protocol turns the test binary into the second process the
// open-lock tests need: it opens the named path with the requested opener and
// blocks until the parent kills it.
const (
	lockHelperEnv      = "RELEVO_LOCK_HELPER"
	lockHelperModeEnv  = "RELEVO_LOCK_HELPER_MODE"
	lockHelperPathEnv  = "RELEVO_LOCK_HELPER_PATH"
	lockHelperReadyEnv = "RELEVO_LOCK_HELPER_READY"
)

// runLockHelper is the test binary acting as a second process: mode "raw" holds
// the engine's own pool on the path, mode "flock" holds relevo's open lock file
// with no engine at all, and both write the ready file and then block until
// killed. The "flock" mode isolates the relevo lock: a raw engine lock would
// make a second open fail for the wrong reason.
func runLockHelper() int {
	path := os.Getenv(lockHelperPathEnv)
	switch os.Getenv(lockHelperModeEnv) {
	case "raw":
		pool, err := db.OpenRaw(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lock helper open:", err)
			return 1
		}
		if perr := pool.Ping(); perr != nil {
			fmt.Fprintln(os.Stderr, "lock helper ping:", perr)
			return 1
		}
	case "flock":
		// The database file exists, so a read-only open reaches the lock
		// instead of stopping at the missing file, but no engine holds it.
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "lock helper create db:", err)
			return 1
		}
		f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lock helper open lock file:", err)
			return 1
		}
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
			fmt.Fprintln(os.Stderr, "lock helper flock:", err)
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "lock helper: unknown mode")
		return 2
	}
	if ready := os.Getenv(lockHelperReadyEnv); ready != "" {
		if werr := os.WriteFile(ready, []byte("ready\n"), 0o600); werr != nil {
			fmt.Fprintln(os.Stderr, "lock helper ready:", werr)
			return 1
		}
	}
	select {}
}

// TestMain joins the transparency switch: when RELEVO_DBTEST_OWNER is set, every
// database this package opens is reached through an in-process owner, and the
// unset default opens the file directly. Before the switch it migrates one
// template database and points Open at it, so a fresh Open copies the migrated
// file instead of running the migrations itself. The order matters twice: the
// helper dispatch runs first, so a child process opens directly, and the
// template is migrated before the owner switch is installed, because a
// hop-opened template would be left with a live owner and a -wal sibling only
// its open handles see, and a copy of the main file alone would miss what the
// WAL still carries.
func TestMain(m *testing.M) {
	if root := os.Getenv(engineHelperRootEnv); root != "" {
		os.Exit(runEngineHelper(root))
	}
	if os.Getenv(lockHelperEnv) != "" {
		os.Exit(runLockHelper())
	}
	templateCleanup, err := installFreshTemplate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ownerCleanup, err := dbtest.OwnerMode()
	if err != nil {
		templateCleanup()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	ownerCleanup()
	templateCleanup()
	os.Exit(code)
}

// installFreshTemplate opens a template database in a fresh temp directory and
// points Open at the migrated file it leaves behind, so a fresh path is seeded
// with a copy of it. cleanup clears the point and removes the directory, so the
// binary leaves nothing behind. The template is closed before it is installed:
// the close checkpoints the WAL, and a copied main file carries the whole
// schema only when that checkpoint left the -wal sibling empty, so a non-empty
// one fails the install rather than seeding a file without its schema.
func installFreshTemplate() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "relevo-db-template-")
	if err != nil {
		return nil, err
	}
	cleanup = func() {
		db.SetFreshTemplate("")
		_ = os.RemoveAll(dir)
	}
	tpl := filepath.Join(dir, "template.db")
	d, err := db.Open(tpl)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := d.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if fi, serr := os.Stat(tpl + "-wal"); serr == nil && fi.Size() > 0 {
		cleanup()
		return nil, fmt.Errorf("db: test template %s has a non-empty -wal sibling (%d bytes)", tpl, fi.Size())
	}
	db.SetFreshTemplate(tpl)
	return cleanup, nil
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
