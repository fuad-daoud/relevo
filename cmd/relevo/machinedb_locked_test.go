//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
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

// rootCommitKey is the settings key remote.RepoID gives dir's root commit,
// the identity the per-repository section is read by.
func rootCommitKey(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-list", "--max-parents=0", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-list --max-parents=0 HEAD in %s: %v", dir, err)
	}
	key, err := remote.RepoID(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("remote.RepoID: %v", err)
	}
	return key
}

// TestBoardReadsThemesThroughTheOwnerWhenTheFileIsHeld pins the board's
// on-locked policy: when the daemon holds the file, the theme resolution
// reads the stored `themes` section and the repository's board_theme through
// the dialled owner, and -- the mutation this catches -- the seed file the
// correction removed is never substituted for either.
func TestBoardReadsThemesThroughTheOwnerWhenTheFileIsHeld(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	served := startTestOwner(t, root)

	// Seed a stored night palette into the `themes` section and a repo
	// board_theme into the machine-local file through the direct handle the
	// owner serves, so a read that reaches the owner sees values distinct
	// from everything the seed carries.
	repoDir := gitRepoWithACommit(t)
	reposBody := fmt.Sprintf(`{%q:{"name":"lab","board_theme":"blueprint"}}`, rootCommitKey(t, repoDir))
	if err := served.db.Local().Tx(func(tx *db.Tx) error {
		if err := tx.ConfigPut("themes", []byte(`{"night":`+themesNight+`}`), time.Now().UTC()); err != nil {
			return err
		}
		return tx.ConfigPut("repos", []byte(reposBody), time.Now().UTC())
	}); err != nil {
		t.Fatalf("seed the sections: %v", err)
	}
	failReadOnlyWithLock(t)

	repo, stored, err := boardThemeSets(repoDir)
	if err != nil {
		t.Fatalf("boardThemeSets: %v, want the stored sets served by the owner", err)
	}
	if repo.BoardTheme != "blueprint" {
		t.Errorf("repo board_theme = %q, want the one the owner served", repo.BoardTheme)
	}
	if len(stored) != 1 {
		t.Errorf("stored sets hold %d palettes, want the section's one stored night, not the seed's two", len(stored))
	}
	if got, ok := stored["night"]; !ok || got != nightPalette() {
		t.Errorf("stored night = %+v, %v; want the stored palette the owner served", got, ok)
	}
	if got := atomic.LoadInt32(&served.ln.accepts); got == 0 {
		t.Errorf("the board read no connection through the owner")
	}

	// The whole resolution rides the same handle: --theme night resolves the
	// section's palette, not the seed's.
	th, err := boardResolveTheme(repoDir, "night")
	if err != nil || th.Name != "night" || th.Accent != "#8be9fd" {
		t.Errorf("--theme night = %v, %v; want the stored palette the owner served", th, err)
	}
}

// TestBoardRefusesAConflictWhenTheFileIsHeldAndNoOwnerAnswers pins that the
// held-file refusal the stored-set read raises reaches the verb as conflict
// naming the held file -- the registry's own refusal, not a seed fallback and
// not a re-wrapped internal. No owner is started, so the dial has nothing to
// answer.
func TestBoardRefusesAConflictWhenTheFileIsHeldAndNoOwnerAnswers(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	if _, err := openDBDirect(machineDBPath()); err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	failReadOnlyWithLock(t)

	want := filepath.Join(root, "relevo", "relevo.db") + ".lock"
	ce := requireCLIError(t, cmdBoard([]string{"--theme", "x"}), codeConflict, "")
	if !strings.Contains(ce.message, want) {
		t.Errorf("refusal %q does not name the held file %q", ce.message, want)
	}
}
