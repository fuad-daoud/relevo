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

// openTestSplit opens the shared/local pair in a directory this test owns, rather
// than in t.TempDir.
//
// The turso driver writes scratch directories of its own -- tmp and turso-go --
// beside whatever database it opens, and it writes them from a thread the Go side
// does not wait for. t.TempDir removes its tree at the end of the test and
// reports a failure when a file lands in the middle of that removal, which makes
// the test fail over an artefact it does not own and did not create. This owns
// the directory and removes it after the handle is closed, retrying a few times
// and then letting the last attempt stand.
func openTestSplit(t *testing.T) (*db.DB, Local) {
	t.Helper()

	dir := ownedDir(t)
	shared, err := db.OpenSplit(filepath.Join(dir, "relevo.db"), db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	local, err := LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	// Registered last, so it runs first: the handle is closed before the
	// directory it holds scratch in is removed.
	t.Cleanup(func() { _ = shared.Close() })
	return shared, local
}

// ownedDir is a directory the test owns and removes itself, for the same reason
// openTestSplit does: anything the driver writes beside a database it opened
// keeps landing after the handle is closed, and t.TempDir would fail the test for
// it. Every path a test hands the driver goes here rather than to t.TempDir.
func ownedDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "relevo-sync-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { removeDriverScratch(dir) })
	return dir
}

// removeDriverScratch removes a test directory, retrying while the driver's own
// scratch is still landing in it. The final attempt's result is the answer, and a
// failure is not reported: what is left behind is the driver's cache, which the
// operating system reclaims and which no assertion depends on.
func removeDriverScratch(dir string) {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = err
}

// seedFixture is a machine-local file with the shared pair beside it. The
// enable path's own fixtures take their preflight from here.
//
// The preflight is a stand-in that passes rather than db.EnablePreflight over a
// real database. That is not a shortcut around a check: the four checks are
// internal/db's own tests, and what these fixtures are about is the seed matrix
// that runs after them. Running the real preflight here would mean writing a
// seed copy to drain the log, and the driver's scratch directories it leaves
// beside the database race with the temp-directory cleanup that ends the test.
func seedFixture(t *testing.T) (*db.DB, Local, func() db.Preflight) {
	t.Helper()

	shared, local := openTestSplit(t)
	return shared, local, func() db.Preflight { return db.Preflight{} }
}

// realPreflight is the preflight over a real database, for the tests whose
// subject is a refusal the checks actually raise. It writes the compress mark
// the checks read and then reaches the asserted upload shape the way a machine
// does: with a seed copy, which is the remedy the check's own message names.
//
// The mark goes to the machine-local file, which is where CompressHistoryOnce
// records it. Writing it through the shared handle would satisfy a check reading
// the wrong file: post-split the pass's marker and the check that reads it have
// to be on the same side of the split, or a database the pass converted goes on
// being refused.
func realPreflight(t *testing.T) (*db.DB, Local, func() db.Preflight) {
	t.Helper()

	shared, local := openTestSplit(t)
	if err := shared.LocalOrSelf().KVPut(compressMarkKey, []byte(`{"at":"2026-10-04T09:00:00Z"}`)); err != nil {
		t.Fatalf("write the compress mark: %v", err)
	}
	drain := filepath.Join(ownedDir(t), "drain.db")
	if err := shared.SeedCopy(drain); err != nil {
		t.Fatalf("SeedCopy: %v", err)
	}
	if err := os.Remove(drain); err != nil {
		t.Fatalf("remove the drain copy: %v", err)
	}
	if pre := db.EnablePreflight(shared); !pre.OK() {
		t.Fatalf("the fixture did not reach the asserted shape: %v", pre.Err())
	}
	return shared, local, func() db.Preflight { return db.EnablePreflight(shared) }
}

// compressMarkKey is the kv row the preflight reads to decide the compress pass
// finished. It is the literal rather than the unexported constant the check
// uses, so this fixture writes the row the check reads.
const compressMarkKey = "zstd-compress.v1"

// remoteFixture is the remote the enable fixtures name. It is a libsql URL the
// validator accepts, so the --url path every fixture now walks is the one a
// first-run user takes rather than a special case only the remote tests reach.
const remoteFixture = "libsql://relevo-test-org.turso.io"

// enableFixture builds an enabler over a fake remote whose answers the test
// sets, and hands back both so a test can read the open it asked for and the
// calls the seed decision made.
func enableFixture(t *testing.T, in SeedInput) (*Enabler, *Fake, *OpenConfig) {
	t.Helper()

	_, local, preflight := seedFixture(t)

	fake := &Fake{}
	var opened OpenConfig
	enabler := &Enabler{
		Local:           local,
		Preflight:       preflight,
		LocalHasHistory: func() (bool, error) { return in.LocalHasHistory, nil },
		CloudEmpty:      func(context.Context, Settings, []byte) (bool, error) { return in.CloudEmpty, nil },
		Intake:          TokenIntake{FromStdin: true, Stdin: []byte(tokenFixture)},
		RemoteURL:       remoteFixture,
		Open: func(_ context.Context, cfg OpenConfig) (SyncClient, error) {
			opened = cfg
			opened.AuthToken = nil
			return fake, nil
		},
		Now:     func() time.Time { return tokenNow },
		Timeout: time.Second,
	}
	return enabler, fake, &opened
}

// TestEnableRefusesAlreadyEnabled pins that a machine already syncing refuses
// rather than running the path again. Enable is not idempotent and must not be:
// the second run would re-decide the seed against a remote the first run has
// already changed, and last-push-wins would then pick a winner by accident.
func TestEnableRefusesAlreadyEnabled(t *testing.T) {
	t.Parallel()

	enabler, fake, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
	if err := MarkEnabled(enabler.Local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}

	if _, err := enabler.Enable(context.Background()); !errors.Is(err, ErrAlreadyEnabled) {
		t.Fatalf("Enable on an enabled machine = %v, want ErrAlreadyEnabled", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("the fake recorded %v, want no calls from a refused enable", fake.Calls)
	}
}

// TestEnableRefusesBadTokenSource pins the two refusals a token can come from.
// A token the caller holds but never routes is refused, and so is a machine with
// no token at all -- and the refusal names both routes without saying which one
// was tried, because the route is half of what an attacker probing the verb is
// asking.
func TestEnableRefusesBadTokenSource(t *testing.T) {
	t.Parallel()

	t.Run("no token on either route", func(t *testing.T) {
		t.Parallel()

		enabler, fake, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
		enabler.Intake = TokenIntake{}

		_, err := enabler.Enable(context.Background())
		if !errors.Is(err, ErrNoToken) {
			t.Fatalf("Enable with no token = %v, want ErrNoToken", err)
		}
		if len(fake.Calls) != 0 {
			t.Errorf("the fake recorded %v, want no calls from a refused enable", fake.Calls)
		}
		// The mark must not have been written: a machine refused for want of a
		// token is a machine that is still off.
		if on, _ := Enabled(enabler.Local); on {
			t.Error("a refused enable marked sync on")
		}
	})

	t.Run("stdin asked for and empty falls through to the environment", func(t *testing.T) {
		t.Parallel()

		enabler, _, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
		enabler.Intake = TokenIntake{FromStdin: true, Stdin: []byte("  \n"), EnvValue: "from-env"}

		if _, err := enabler.Enable(context.Background()); err != nil {
			t.Fatalf("Enable: %v", err)
		}
		stored, ok, err := ReadToken(enabler.Local)
		if err != nil || !ok {
			t.Fatalf("ReadToken = %v, %v", ok, err)
		}
		if string(stored) != "from-env" {
			t.Errorf("stored token = %q, want the environment's", stored)
		}
	})
}

// TestEnableStoresAndOpensTheFlaggedRemote pins the whole of --url: an enable
// that named a remote writes it into the machine-local section, and the open it
// then makes carries that same remote rather than an empty one.
//
// The two halves are one behavior. Storing without opening would leave a
// section claiming a remote no handle ever used, and opening without storing
// would make the enable depend on a flag the next push cannot see -- so the
// assertion reads both the section and the open the fake was handed.
func TestEnableStoresAndOpensTheFlaggedRemote(t *testing.T) {
	t.Parallel()

	enabler, _, opened := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})

	res, err := enabler.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}

	stored, err := ReadSettings(enabler.Local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	if stored.RemoteURL != remoteFixture {
		t.Errorf("stored remote_url = %q, want %q", stored.RemoteURL, remoteFixture)
	}
	if opened.RemoteURL != remoteFixture {
		t.Errorf("the open's RemoteURL = %q, want the stored %q", opened.RemoteURL, remoteFixture)
	}
	if res.RemoteURL != remoteFixture {
		t.Errorf("result RemoteURL = %q, want %q", res.RemoteURL, remoteFixture)
	}
}

// TestEnableRefusesAContradictingRemoteURL pins that --url never silently
// repoints a machine. The stored remote is one this installation has been
// pushing to; overwriting it with a second URL would move every row to a
// database the user did not name, so the enable refuses and the refusal names
// both so the reader can tell which is which.
func TestEnableRefusesAContradictingRemoteURL(t *testing.T) {
	t.Parallel()

	const other = "libsql://somewhere-else.turso.io"

	enabler, fake, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
	if err := PutSettings(enabler.Local, Settings{RemoteURL: remoteFixture}, tokenNow); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	enabler.RemoteURL = other

	_, err := enabler.Enable(context.Background())
	if !errors.Is(err, ErrRemoteConflict) {
		t.Fatalf("Enable with a contradicting --url = %v, want ErrRemoteConflict", err)
	}
	for _, want := range []string{remoteFixture, other} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if len(fake.Calls) != 0 {
		t.Errorf("the fake recorded %v, want no calls from a refused enable", fake.Calls)
	}
	stored, err := ReadSettings(enabler.Local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	if stored.RemoteURL != remoteFixture {
		t.Errorf("a refused enable changed the stored remote to %q", stored.RemoteURL)
	}
}

// TestEnableRefusesWithNoRemoteAnywhere pins the first-run refusal: a machine
// with no stored remote and no --url has nothing to open, and the message names
// the flag rather than sending the reader to the config document.
func TestEnableRefusesWithNoRemoteAnywhere(t *testing.T) {
	t.Parallel()

	enabler, fake, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
	enabler.RemoteURL = ""

	_, err := enabler.Enable(context.Background())
	if !errors.Is(err, ErrNoRemote) {
		t.Fatalf("Enable with no remote anywhere = %v, want ErrNoRemote", err)
	}
	if !strings.Contains(err.Error(), "--url") {
		t.Errorf("the refusal does not name the flag: %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("the fake recorded %v, want no calls from a refused enable", fake.Calls)
	}
}

// TestEnableRefusesAnInvalidRemoteURL pins that --url is checked by the same
// validator the section is, so a value the stored path would refuse cannot be
// reached by typing it on a command line.
func TestEnableRefusesAnInvalidRemoteURL(t *testing.T) {
	t.Parallel()

	enabler, fake, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
	enabler.RemoteURL = "not a url"

	_, err := enabler.Enable(context.Background())
	if !errors.Is(err, db.ErrInvalid) {
		t.Fatalf("Enable with a garbage --url = %v, want the invalid refusal", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("the fake recorded %v, want no calls from a refused enable", fake.Calls)
	}
}

// TestTokenNeverLeavesMachineOnARefusal pins the one promise the token surface
// makes on the enable path: once a token has been read from a route, a refusal
// further down names neither the value nor the route it came by. The refusal is
// the line that reaches a log, so either in it would leak through the one
// surface that is always written. Naming the routes that exist is a different
// thing and is allowed -- a user who passed --token-stdin needs to be told what
// else they could have done.
func TestTokenNeverLeavesMachineOnARefusal(t *testing.T) {
	t.Parallel()

	for _, route := range []TokenSource{TokenSourceStdin, TokenSourceEnv} {
		t.Run(string(route), func(t *testing.T) {
			t.Parallel()

			intake := TokenIntake{EnvValue: "from-env"}
			if route == TokenSourceStdin {
				intake = TokenIntake{FromStdin: true, Stdin: []byte(tokenFixture)}
			}
			resolved, got, err := intake.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != route || string(resolved) == "" {
				t.Fatalf("Resolve = %q, %v; want the %q route", resolved, got, route)
			}

			enabler, _, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
			enabler.Intake = intake
			// A preflight that refuses is the ordinary refusal path, and it runs
			// after the token has been resolved -- so this is where a careless
			// implementation would format the value or the route into the line.
			enabler.Preflight = func() db.Preflight {
				return db.Preflight{Refusals: []db.PreflightRefusal{{Check: "fixture", Detail: "a check that refuses"}}}
			}

			_, err = enabler.Enable(context.Background())
			if err == nil {
				t.Fatal("Enable with a refusing preflight returned nil, want a refusal")
			}
			if strings.Contains(err.Error(), tokenFixture) {
				t.Errorf("the refusal carries the token value: %v", err)
			}
			if strings.Contains(err.Error(), string(route)) {
				t.Errorf("the refusal names the %q route the token came by: %v", route, err)
			}
		})
	}
}

// TestEmptyCloudFirstPushIsSeed pins the first arm of the seed matrix: history
// here and nothing on the remote, so the open skips the bootstrap and the first
// push is what seeds it. The open is what makes this an arm rather than a
// slogan -- a decision that pushed without saying BootstrapIfEmpty=false would
// still push, and would also have asked the driver to fetch first.
func TestEmptyCloudFirstPushIsSeed(t *testing.T) {
	t.Parallel()

	enabler, fake, opened := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})

	res, err := enabler.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if res.Case != SeedEmptyCloud {
		t.Errorf("case = %q, want %q", res.Case, SeedEmptyCloud)
	}
	if opened.BootstrapIfEmpty {
		t.Error("BootstrapIfEmpty = true; an empty remote has nothing to bootstrap from")
	}
	if len(fake.Calls) == 0 || fake.Calls[0] != "push" {
		t.Errorf("calls = %v, want a push first", fake.Calls)
	}
	if !contains(fake.Calls, "pull") {
		t.Errorf("calls = %v, want a pull: an open that skipped the bootstrap owes one", fake.Calls)
	}
}

// TestExistingDBUploadThenEnable pins the arm that cannot proceed on its own:
// history on both sides refuses, writes the seed copy the documented upload path
// takes, and names that path. The second enable -- the one told the upload
// happened -- proceeds, which is what makes the refusal an instruction rather
// than a wall.
func TestExistingDBUploadThenEnable(t *testing.T) {
	t.Parallel()

	shared, local, preflight := realPreflight(t)

	seedPath := filepath.Join(ownedDir(t), "seed.db")
	var copies []string
	fake := &Fake{}
	newEnabler := func(uploaded bool) *Enabler {
		return &Enabler{
			Local:           local,
			Preflight:       preflight,
			LocalHasHistory: func() (bool, error) { return true, nil },
			CloudEmpty:      func(context.Context, Settings, []byte) (bool, error) { return false, nil },
			Intake:          TokenIntake{FromStdin: true, Stdin: []byte(tokenFixture)},
			RemoteURL:       remoteFixture,
			SeedCopy: func(path string) error {
				copies = append(copies, path)
				return shared.SeedCopy(path)
			},
			SeedPath:     seedPath,
			SeedUploaded: uploaded,
			Open:         func(context.Context, OpenConfig) (SyncClient, error) { return fake, nil },
			Now:          func() time.Time { return tokenNow },
			Timeout:      time.Second,
		}
	}

	res, err := newEnabler(false).Enable(context.Background())
	if !errors.Is(err, ErrSeedUploadRequired) {
		t.Fatalf("Enable with history on both sides = %v, want ErrSeedUploadRequired", err)
	}
	if len(copies) != 1 || copies[0] != seedPath {
		t.Errorf("seed copies written = %v, want one at %s", copies, seedPath)
	}
	if !strings.Contains(err.Error(), seedPath) {
		t.Errorf("the refusal does not name the seed copy: %v", err)
	}
	if res.Seed != seedPath {
		t.Errorf("result seed = %q, want %q", res.Seed, seedPath)
	}
	if _, serr := os.Stat(seedPath); serr != nil {
		t.Errorf("the seed copy is not on disk: %v", serr)
	}

	// The upload has happened; the same machine now enables.
	res, err = newEnabler(true).Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable after the upload: %v", err)
	}
	if res.Case != SeedExistingDB {
		t.Errorf("case = %q, want %q", res.Case, SeedExistingDB)
	}
	if len(fake.Calls) == 0 || fake.Calls[0] != "push" {
		t.Errorf("calls = %v, want a push first", fake.Calls)
	}
	if !contains(fake.Calls, "pull") {
		t.Errorf("calls = %v, want a pull after the skipped bootstrap", fake.Calls)
	}
}

// TestNewMachineBootstrapPull pins the arm that is the reverse of the empty
// cloud: nothing here, so the remote is the only source and the open takes its
// initial state. The pull is explicit even though the bootstrap already performs
// one, because a remote that moves between the two must not leave this machine a
// round behind with no call that said so.
func TestNewMachineBootstrapPull(t *testing.T) {
	t.Parallel()

	enabler, fake, opened := enableFixture(t, SeedInput{LocalHasHistory: false, CloudEmpty: false})
	fake.Applied = true

	res, err := enabler.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if res.Case != SeedNewMachine {
		t.Errorf("case = %q, want %q", res.Case, SeedNewMachine)
	}
	if !opened.BootstrapIfEmpty {
		t.Error("BootstrapIfEmpty = false; a machine with no history has only the remote to read")
	}
	if len(fake.Calls) == 0 || fake.Calls[0] != "pull" {
		t.Errorf("calls = %v, want a pull first", fake.Calls)
	}
	if contains(fake.Calls, "push") {
		t.Errorf("calls = %v, want no push: there is nothing here to seed with", fake.Calls)
	}
	if !res.Applied {
		t.Error("result says nothing was applied, want the pull's own answer")
	}
}

// TestEnableMarksOnlyAfterTheSeedDecision pins where the mark lands: after
// every refusal and after the token is stored, so a machine refused at any step
// is still off and holds no credential. It is the ordering the whole path rests
// on -- a mark written first would make a refusal leave a machine that reads as
// syncing and cannot.
func TestEnableMarksOnlyAfterTheSeedDecision(t *testing.T) {
	t.Parallel()

	enabler, _, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: false})
	enabler.SeedCopy = func(path string) error { return sharedNoop(t) }
	enabler.SeedPath = filepath.Join(ownedDir(t), "seed.db")
	enabler.Open = nil

	if _, err := enabler.Enable(context.Background()); !errors.Is(err, ErrSeedUploadRequired) {
		t.Fatalf("Enable = %v, want ErrSeedUploadRequired", err)
	}
	if on, _ := Enabled(enabler.Local); on {
		t.Error("a refused enable marked sync on")
	}
	if _, ok, _ := ReadToken(enabler.Local); ok {
		t.Error("a refused enable stored a token")
	}
}

// TestReEnableIsFreshEnable pins that turning sync off and back on runs the whole
// path again. There is no resume: a re-enable re-decides the seed against the
// remote as it is now, which is the only way an enable can be right about a
// remote that changed while this machine was away.
func TestReEnableIsFreshEnable(t *testing.T) {
	t.Parallel()

	enabler, fake, _ := enableFixture(t, SeedInput{LocalHasHistory: true, CloudEmpty: true})
	if _, err := enabler.Enable(context.Background()); err != nil {
		t.Fatalf("the first enable: %v", err)
	}

	disabler := &Disabler{Local: enabler.Local, Client: fake, Now: func() time.Time { return tokenNow }, Timeout: time.Second}
	if _, err := disabler.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// The remote now holds what this machine pushed, so the second enable meets
	// history on both sides. A resume would carry the first run's decision
	// across and push again; the fresh path re-decides and refuses.
	enabler.CloudEmpty = func(context.Context, Settings, []byte) (bool, error) { return false, nil }
	if _, err := enabler.Enable(context.Background()); !errors.Is(err, ErrSeedUploadRequired) {
		t.Fatalf("the second enable = %v, want the fresh seed decision to refuse", err)
	}
}

// sharedNoop is a seed-copy writer for a test that only cares about ordering:
// it has to exist so the path gets past the upload refusal, and it writes
// nothing because the refusal under test happens either side of it.
func sharedNoop(*testing.T) error { return nil }

// contains reports whether calls holds want.
func contains(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
