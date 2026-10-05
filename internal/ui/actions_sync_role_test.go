package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The cockpit's connection test is the one sync open that does not go through
// the executor: it asks the remote a question and reports the answer, so it is
// not a verb. That makes it the sibling most able to drift from the others,
// because nothing in the daemon's path shares its builder.
//
// So its role is pinned two ways: by what the open does to a file the driver
// has never joined, and by what the builder says it names.

// TestTheConnectionTestRefusesABareFile pins that the cockpit's connection test
// asks as a member.
//
// It names the live file, and the question it asks must never create sync
// membership in it. A test that could join a machine to a remote as a side
// effect of being asked whether the remote is up would be the same class of
// mistake the emptiness probe made in the other direction, and it would reach
// the user as a successful test on a machine that had just been changed.
//
// The refusal is asserted through the real gate rather than a stand-in, because
// the gate is what the cockpit's open actually meets: the open is built inline
// in openSync with no seam to inject through, so the only way to see the guard
// work is to drive the driver route on the config that open describes.
func TestTheConnectionTestRefusesABareFile(t *testing.T) {
	a := syncRealActions(t)
	shared := a.runtime().DB
	writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	// The fixture's file is a bare file: this build opened it and no driver ever
	// joined it, which is the state a machine is in until an enable has run. A
	// connection test on such a machine must say so rather than join it.
	ok, err := relevosync.HasSyncMarker(shared.Path())
	if err != nil {
		t.Fatalf("HasSyncMarker on the fixture's own file: %v", err)
	}
	if ok {
		t.Fatal("the fixture's file already carries a sync marker, so it is not the bare file this test is about")
	}
	if err := relevosync.RequireSyncMember(shared.Path()); !errors.Is(err, relevosync.ErrNotSynced) {
		t.Fatalf("RequireSyncMember on a bare file = %v, want ErrNotSynced", err)
	}

	before := uiRoleHash(t, shared.Path())

	// The gate is driven on the config the cockpit builds: the live path, a
	// member role, nothing bootstrapped. It refuses here, before the driver is
	// given the path, which is the ordering the whole guard exists for.
	_, oerr := relevosync.OpenRemote(t.Context(), relevosync.OpenConfig{
		Role:             relevosync.OpenMember,
		Path:             shared.Path(),
		RemoteURL:        syncRemote,
		ClientName:       syncClientName,
		AuthToken:        []byte(syncSecretValue),
		BootstrapIfEmpty: false,
	})
	if !errors.Is(oerr, relevosync.ErrNotSynced) {
		t.Errorf("a connection test over a bare file = %v, want ErrNotSynced", oerr)
	}

	if after := uiRoleHash(t, shared.Path()); after != before {
		t.Errorf("the membership guard changed the file it was asked about: %s -> %s", before, after)
	}
}

// TestTheCockpitNamesTheMemberRoleOnItsOwnOpen pins the role the cockpit's
// inline open carries, by reading this package's own source.
//
// It is a grep rather than a behavioural assertion because the hazard is
// textual: this open is built inline with no seam to inject through, so nothing
// short of reading the builder can say what role it will arrive carrying. The
// test above says the member role refuses a bare file; this says the builder is
// why, so a rebuild of that literal which drops the role cannot pass on the
// behaviour alone.
func TestTheCockpitNamesTheMemberRoleOnItsOwnOpen(t *testing.T) {
	body, err := os.ReadFile("actions_sync.go")
	if err != nil {
		t.Fatalf("read actions_sync.go: %v", err)
	}

	start := strings.Index(string(body), "func (a *mastermindActions) openSync(")
	if start < 0 {
		t.Fatal("openSync is gone, so the cockpit's own open has no builder to read")
	}
	rest := string(body)[start:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		end = len(rest)
	}
	fn := rest[:end]

	if !strings.Contains(fn, "relevosync.OpenMember") {
		t.Errorf("the cockpit's own open does not name the member role:\n%s", fn)
	}
	// A scratch or seed role here would let a question create sync membership in
	// the live file, which is the one thing a question must never do.
	for _, wrong := range []string{"relevosync.OpenScratch", "relevosync.OpenSeed"} {
		if strings.Contains(fn, wrong) {
			t.Errorf("the cockpit's own open names %s, which may create membership", wrong)
		}
	}
}

// uiRoleHash is a file's size and content hash, which together are what says the
// membership guard changed nothing about the file it was asked about.
func uiRoleHash(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(body)
	return strconv.Itoa(len(body)) + " bytes " + hex.EncodeToString(sum[:8])
}
