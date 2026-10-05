package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The tests here pin the two properties that keep a sync open from rewriting a
// file it should not: a probe names a throwaway, and a live open names a file
// the driver has already joined.

// TestThrowawayIsEmptyPrivateAndDisposable pins what a probe is allowed to
// name: a file this process made, empty, in a directory only this user reaches,
// and gone once released.
//
// The emptiness and the owner-only mode are what make it safe to let the driver
// open one -- it is going to be rewritten, and nothing of the caller's is in it.
func TestThrowawayIsEmptyPrivateAndDisposable(t *testing.T) {
	t.Parallel()

	beside := filepath.Join(probeOwnedDir(t), "relevo.db")
	scratch, err := NewThrowaway(beside)
	if err != nil {
		t.Fatalf("NewThrowaway: %v", err)
	}

	info, err := os.Stat(scratch.Path)
	if err != nil {
		t.Fatalf("stat the throwaway: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("the throwaway holds %d bytes, want an empty file", info.Size())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the throwaway is mode %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(scratch.Path))
	if err != nil {
		t.Fatalf("stat the throwaway's directory: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("the throwaway's directory is mode %o, want 700", perm)
	}

	// It is not the file it sits beside, whatever the caller meant.
	if scratch.Path == beside {
		t.Error("the throwaway is the live path")
	}

	scratch.Release()
	if _, err := os.Stat(scratch.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the throwaway outlived Release: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(scratch.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the throwaway's directory outlived Release: %v", err)
	}

	// Releasing again is not an error: a probe's cleanup sits on every path out
	// of it, including the ones that failed.
	scratch.Release()
}

// TestThrowawayRefusesWithNoPathToSitBeside pins that a throwaway needs
// somewhere to live. Without it the path would be resolved against the process's
// working directory, which is not a directory this package may assume.
func TestThrowawayRefusesWithNoPathToSitBeside(t *testing.T) {
	t.Parallel()

	if _, err := NewThrowaway(""); err == nil {
		t.Error("NewThrowaway with no path succeeded, want a refusal")
	}
	var nilScratch *Throwaway
	nilScratch.Release()
}

// TestHasSyncMarkerAnswersForEveryFile pins the question the guard asks, over
// the three answers it can give: a file, a file that is not there, and a path
// with no name.
//
// The absent case matters as much as the present one. An open against a path
// that does not exist is an open that would create one, so it must read as "not
// a member" rather than as an error the caller has to interpret.
func TestHasSyncMarkerAnswersForEveryFile(t *testing.T) {
	t.Parallel()

	dir := probeOwnedDir(t)
	shared, _ := probeSplit(t, dir)

	// A database this build opened and no driver joined: a real file, no marker.
	present, err := HasSyncMarker(shared.Path())
	if err != nil {
		t.Fatalf("HasSyncMarker on a real file: %v", err)
	}
	if present {
		t.Error("a file no driver opened reports a sync marker")
	}

	absent, err := HasSyncMarker(filepath.Join(dir, "never-written.db"))
	if err != nil {
		t.Errorf("HasSyncMarker on an absent file = %v, want no marker and no error", err)
	}
	if absent {
		t.Error("an absent file reports a sync marker")
	}

	if _, err := HasSyncMarker(""); !errors.Is(err, ErrNotSynced) {
		t.Errorf("HasSyncMarker with no path = %v, want ErrNotSynced", err)
	}
	if err := RequireSyncMember(filepath.Join(dir, "never-written.db")); !errors.Is(err, ErrNotSynced) {
		t.Errorf("RequireSyncMember on an absent file = %v, want ErrNotSynced", err)
	}
}

// TestHasSyncMarkerFindsAMarkerTable pins that the check can actually answer yes.
// Without a file that does carry one, the refusal above would pass on a check
// that never matches anything -- which is how a guard that refuses everything
// looks green.
func TestHasSyncMarkerFindsAMarkerTable(t *testing.T) {
	t.Parallel()

	dir := probeOwnedDir(t)
	path := filepath.Join(dir, "member.db")
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	for _, table := range syncMarkerTables {
		if _, err := raw.Exec(`CREATE TABLE ` + table + ` (client_id TEXT PRIMARY KEY)`); err != nil {
			_ = raw.Close()
			t.Fatalf("create %s: %v", table, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ok, err := HasSyncMarker(path)
	if err != nil {
		t.Fatalf("HasSyncMarker on a member file: %v", err)
	}
	if !ok {
		t.Errorf("a file carrying %v reports no sync marker", syncMarkerTables)
	}
	if err := RequireSyncMember(path); err != nil {
		t.Errorf("RequireSyncMember on a member file = %v, want nil", err)
	}
}

// TestAnOpenWithNoRoleRefuses pins the zero value. An open that did not say
// whether it may create sync membership has to be stopped: the whole hazard is
// an open reaching a bare file, and a caller that forgot the question is
// exactly the caller that must not be given the benefit of the doubt.
func TestAnOpenWithNoRoleRefuses(t *testing.T) {
	t.Parallel()

	err := checkOpenRole(OpenConfig{Path: "/tmp/whatever.db"})
	if !errors.Is(err, ErrNotSynced) {
		t.Errorf("an open with no role = %v, want ErrNotSynced", err)
	}
}

// TestRoleDecidesWhetherMembershipMayBeCreated pins what each role permits. The
// seed open is the one that may create membership, because it is the enable
// that creates it; the scratch open may because its file is disposable; and
// nothing else may, because a file the driver has not joined must never be
// turned into one by a caller that expected it to be one already.
func TestRoleDecidesWhetherMembershipMayBeCreated(t *testing.T) {
	t.Parallel()

	dir := probeOwnedDir(t)
	bare := filepath.Join(dir, "bare.db")
	shared, _ := probeSplit(t, dir)

	cases := []struct {
		name string
		cfg  OpenConfig
		want error
	}{
		{"a scratch open needs nothing", OpenConfig{Role: OpenScratch, Path: bare}, nil},
		{"a seed open creates membership", OpenConfig{Role: OpenSeed, Path: bare}, nil},
		{"a member open over a bare file refuses", OpenConfig{Role: OpenMember, Path: bare}, ErrNotSynced},
		// The live shared file is a bare file too: this build opened it and no
		// driver ever joined it, which is the state a machine is in until an
		// enable has run. Naming it as a member must refuse.
		{"a member open over the live shared file refuses", OpenConfig{Role: OpenMember, Path: shared.Path()}, ErrNotSynced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkOpenRole(tc.cfg)
			if tc.want == nil {
				if err != nil {
					t.Errorf("checkOpenRole = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("checkOpenRole = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestTheUploadRefusalNamesTheWorkingCommand pins the refusal text. The shipped
// turso CLI takes the file as a positional and prompts for the target database,
// so the message has to say that; a message naming a flag form the CLI does not
// have names a command the user cannot run, and the refusal is the only place
// they are told what to do.
func TestTheUploadRefusalNamesTheWorkingCommand(t *testing.T) {
	t.Parallel()

	shared, local, preflight := realPreflight(t)
	seed := filepath.Join(ownedDir(t), "seed.db")
	putSettings(t, local, remoteFixture)

	enabler := &Enabler{
		Local:           local,
		Preflight:       preflight,
		LocalHasHistory: func() (bool, error) { return true, nil },
		// The remote already holds data, so history on both sides is the arm that
		// refuses and names the upload.
		CloudEmpty: func(context.Context, Settings, []byte) (bool, error) { return false, nil },
		Intake:     TokenIntake{FromStdin: true, Stdin: []byte(tokenFixture)},
		SeedCopy:   func(path string) error { return shared.SeedCopy(path) },
		SeedPath:   seed,
		Open:       func(context.Context, OpenConfig) (SyncClient, error) { return &Fake{}, nil },
		Now:        func() time.Time { return tokenNow },
		Timeout:    time.Second,
	}

	_, err := enabler.Enable(context.Background())
	if !errors.Is(err, ErrSeedUploadRequired) {
		t.Fatalf("Enable with history on both sides = %v, want ErrSeedUploadRequired", err)
	}
	want := "upload " + seed + " with `turso db import " + seed + "`"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not name the working command and the seed path.\n got: %v\nwant it to contain: %s", err, want)
	}
	if strings.Contains(err.Error(), "--from-file") {
		t.Errorf("the refusal names a flag form the CLI does not have: %v", err)
	}
	if !strings.Contains(err.Error(), "--seed-uploaded") {
		t.Errorf("the refusal does not say how to come back: %v", err)
	}
}

// probeOwnedDir is a directory this test owns and removes itself, rather than
// t.TempDir: the turso driver writes scratch of its own beside a file it opens,
// from a thread this side does not wait for, and t.TempDir fails a test over a
// file landing in the middle of its own removal.
func probeOwnedDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "relevo-probe-own-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { removeDriverScratch(dir) })
	return dir
}

// probeSplit opens a shared/local pair in a directory this test owns.
func probeSplit(t *testing.T, dir string) (*db.DB, Local) {
	t.Helper()

	shared, err := db.OpenSplit(filepath.Join(dir, "relevo.db"), db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return shared, local
}

// putSettings writes the sync section's remote URL.
func putSettings(t *testing.T, local Local, remote string) {
	t.Helper()

	if err := PutSettings(local, Settings{RemoteURL: remote}, tokenNow); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
}
