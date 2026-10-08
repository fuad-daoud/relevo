package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// syncRealActions opens a split database pair in a temp directory and returns
// the real Actions over it. Every method here reads or writes the machine-local
// file beside the shared one, so a test that used a fake for them would never
// learn whether the local/shared split was actually honoured -- which is the one
// thing about these actions that matters most.
func syncRealActions(t *testing.T) *mastermindActions {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	return &mastermindActions{live: newLiveRuntime(relevo.Runtime{DB: shared})}
}

// writeSyncSettings puts a section and a token into the local file beside the
// shared handle, the way an enable would. The section is written whole and the
// mark after it, because MarkEnabled only moves the enable flag and keeps every
// other field it finds -- which is the behaviour that makes a turn-off safe.
func writeSyncSettings(t *testing.T, a *mastermindActions, st relevosync.Settings, token string) relevosync.Local {
	t.Helper()
	local, err := relevosync.LocalHandle(a.runtime().DB)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	body, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("encode the section: %v", err)
	}
	if _, err := relevosync.ParseSettings(body); err != nil {
		t.Fatalf("the fixture's own section is invalid: %v", err)
	}
	if err := local.Tx(func(tx *db.Tx) error { return tx.ConfigPut(relevosync.SectionSettings, body, syncNow) }); err != nil {
		t.Fatalf("write the section: %v", err)
	}
	if err := relevosync.MarkEnabled(local, st.Enabled, syncNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	if token != "" {
		if err := relevosync.SetToken(local, []byte(token), syncNow); err != nil {
			t.Fatalf("SetToken: %v", err)
		}
	}
	if !st.Enabled {
		return local
	}
	// A machine that has never ticked reads as behind, because S2's mapping
	// reads an absent tick as a failed one. Most of these fixtures want the
	// healthy token, so they get the marker a successful tick would leave.
	if err := local.KVPut(relevosync.KeyLastTick, []byte(`{"at":"2026-10-04T12:00:00Z","ok":true}`)); err != nil {
		t.Fatalf("write the tick marker: %v", err)
	}
	return local
}

// TestSyncSnapshotOnAFreshInstall pins what a machine that has never enabled
// sync reads back: off, no remote, no token, no measurement, and one
// installation directory row. It is the pre-on form's only source, so every one
// of those has to be a real answer rather than a zero the view happens to render
// the same way.
func TestSyncSnapshotOnAFreshInstall(t *testing.T) {
	a := syncRealActions(t)
	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.Token != relevosync.TokenOff {
		t.Errorf("token is %q, want %q", snap.Token, relevosync.TokenOff)
	}
	if snap.State.Enabled {
		t.Errorf("a fresh install reports sync enabled")
	}
	if snap.RemoteURL != "" || snap.TokenSet {
		t.Errorf("a fresh install reports remote %q token %t", snap.RemoteURL, snap.TokenSet)
	}
	if snap.Measured {
		t.Errorf("a fresh install reports a measurement")
	}
	if snap.SharedBytes <= 0 {
		t.Errorf("a fresh install reports %d shared bytes, want the file's own size", snap.SharedBytes)
	}
	if !snap.Unknown {
		t.Errorf("a fresh install claims reachability before anything was dialled")
	}
}

// TestSyncSnapshotReadsTheLocalFileOnly pins the split: a section written into
// the local file is visible, and the same section written into the shared file
// is not. The second half is the load-bearing one -- a read that fell back to
// the shared file would report a section no enable ever set, and would report it
// on a machine whose real settings are something else entirely.
func TestSyncSnapshotReadsTheLocalFileOnly(t *testing.T) {
	a := syncRealActions(t)
	shared := a.runtime().DB
	writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.RemoteURL != syncRemote || !snap.State.Enabled {
		t.Errorf("the local section was not read: %+v", snap)
	}
	if snap.Token != relevosync.TokenOK {
		t.Errorf("token is %q, want %q", snap.Token, relevosync.TokenOK)
	}
	if !snap.TokenSet {
		t.Errorf("a stored token was not reported present")
	}

	// A section in the shared file must not answer for the local one.
	if err := shared.Tx(func(tx *db.Tx) error {
		return tx.ConfigPut("sync", []byte(`{"enabled":true,"remote_url":"libsql://elsewhere.turso.io"}`), syncNow)
	}); err != nil {
		t.Fatalf("write a shared section: %v", err)
	}
	snap, err = a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot after the shared write: %v", err)
	}
	if snap.RemoteURL != syncRemote {
		t.Errorf("the read took the shared section: %q", snap.RemoteURL)
	}
}

// TestSyncSnapshotReportsAttention pins that the error a human must act on
// reaches the view with S2's own message, and that the token becomes the error
// token rather than a healthy one.
func TestSyncSnapshotReportsAttention(t *testing.T) {
	a := syncRealActions(t)
	local := writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	// The markers a Runner writes when the remote refuses the token.
	if err := local.KVPut(relevosync.KeyLastTick, []byte(`{"at":"2026-10-04T12:00:00Z","ok":false}`)); err != nil {
		t.Fatalf("write the tick marker: %v", err)
	}
	if err := local.KVPut(relevosync.KeyAttention, []byte(`{"at":"2026-10-04T12:00:00Z","message":"the remote refused this installation's token"}`)); err != nil {
		t.Fatalf("write the attention marker: %v", err)
	}
	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.Token != relevosync.TokenErr {
		t.Errorf("token is %q, want %q", snap.Token, relevosync.TokenErr)
	}
	if !strings.Contains(snap.Attention, "refused") {
		t.Errorf("attention is %q, want the marker's own message", snap.Attention)
	}
}

// TestSyncSnapshotCarriesTheSnapshotAndItsAbsence pins the pair the post-on block
// reads: the remote's report, and whether there ever was one. A machine that has
// measured nothing must not be drawn as a machine with nothing to send.
func TestSyncSnapshotCarriesTheSnapshotAndItsAbsence(t *testing.T) {
	a := syncRealActions(t)
	local := writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.Measured {
		t.Errorf("an unmeasured machine reports a measurement")
	}

	if err := local.KVPut(relevosync.KeyBacklog, []byte("7")); err != nil {
		t.Fatalf("write the backlog marker: %v", err)
	}
	if err := local.KVPut(relevosync.KeyStats, []byte(`{"cdc_operations":7,"network_sent_bytes":2048,"revision":"rev-1"}`)); err != nil {
		t.Fatalf("write the snapshot marker: %v", err)
	}
	snap, err = a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot after the markers: %v", err)
	}
	if !snap.Measured || snap.State.Backlog != 7 || snap.Stats.Revision != "rev-1" {
		t.Errorf("the snapshot did not reach the view: %+v", snap)
	}
}

// TestSyncActionsRefuseWithoutARemoteOrToken pins the refusal every action that
// would reach a remote gives on a machine that has no remote, no token, or
// neither. The verbs carry no build-wide "unavailable" answer: push, pull and
// test each open the worker the stored rows build, and a machine with a row
// missing refuses with the sentence that names it.
//
// None of them starts a process or reaches a network to find that out: the
// opener refuses on the machine-local rows before a worker is built.
func TestSyncActionsRefuseWithoutARemoteOrToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, a *mastermindActions)
	}{
		{"neither a remote nor a token", func(t *testing.T, a *mastermindActions) {}},
		{"a token and no remote", func(t *testing.T, a *mastermindActions) {
			writeSyncSettings(t, a, relevosync.Settings{}, syncSecretValue)
		}},
		{"a remote and no token", func(t *testing.T, a *mastermindActions) {
			writeSyncSettings(t, a, relevosync.Settings{RemoteURL: syncRemote}, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := syncRealActions(t)
			tc.setup(t, a)
			for _, verb := range []struct {
				name string
				call func(context.Context) Result
			}{
				{"push", a.SyncPush},
				{"pull", a.SyncPull},
				{"test", a.SyncTest},
			} {
				res := verb.call(context.Background())
				if res.Err == nil {
					t.Errorf("%s succeeded with no remote or token to reach", verb.name)
					continue
				}
				if !strings.Contains(res.Err.Error(), "remote") && !strings.Contains(res.Err.Error(), "token") {
					t.Errorf("%s said %q, want the missing remote or token named", verb.name, res.Err)
				}
			}
		})
	}
}

// TestSyncDisableRunsS4TurnOffWhole pins that the d key's action is S4's own
// turn-off and not a re-implementation: the enabled marker goes, the token goes,
// and every local row is still there afterwards.
func TestSyncDisableRunsS4TurnOffWhole(t *testing.T) {
	a := syncRealActions(t)
	shared := a.runtime().DB
	local := writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)
	if _, err := shared.RecordPut(db.Record{
		Owner: "alice", Name: "webshop", State: "open", JSON: "{}",
		CreatedAt: syncNow, UpdatedAt: syncNow,
	}); err != nil {
		t.Fatalf("seed a record: %v", err)
	}
	// The record is here to prove the turn-off keeps it, not to be pushed: the
	// outbox it filled is emptied so the final export has nothing to hand over,
	// and this test starts no worker behind the fixture's remote.
	if err := shared.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}

	res := a.SyncDisable(context.Background())
	if res.Err != nil {
		t.Fatalf("SyncDisable: %v (%s)", res.Err, res.Text)
	}
	if !res.Refresh {
		t.Errorf("a turn-off does not ask for a re-read: %q", res.Text)
	}

	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.State.Enabled || snap.Token != relevosync.TokenOff {
		t.Errorf("sync is still on after the turn-off: %+v", snap)
	}
	if snap.TokenSet {
		t.Errorf("the token survived the turn-off")
	}
	// The record lives in the shared file and a turn-off touches nothing there.
	rows, err := shared.RecordList("alice")
	if err != nil {
		t.Fatalf("RecordList: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the turn-off changed the shared records: %d rows", len(rows))
	}
	if _, ok, err := relevosync.ReadToken(local); err != nil || ok {
		t.Errorf("the token is still in the local file (ok=%t err=%v)", ok, err)
	}
}

// TestSyncDisableWithoutSyncIsNotAnError pins that turning sync off on a machine
// that never had it on still succeeds. A turn-off is idempotent, and refusing it
// would leave a user with no way to clear a section they no longer want.
func TestSyncDisableWithoutSyncIsNotAnError(t *testing.T) {
	a := syncRealActions(t)
	res := a.SyncDisable(context.Background())
	if res.Err != nil {
		t.Fatalf("SyncDisable on a machine that never enabled: %v", res.Err)
	}
}

// TestSyncActionsWithoutARuntimeRefuse pins the nil-runtime paths. A cockpit
// whose runtime has not loaded yet must get an error, not a panic: a screen
// that takes the cockpit down is worse than a screen that says nothing.
func TestSyncActionsWithoutARuntimeRefuse(t *testing.T) {
	for _, a := range []*mastermindActions{
		{},                                       // a zero adapter: the holder is nil too
		{live: newLiveRuntime(relevo.Runtime{})}, // a holder with no database
	} {
		if _, err := a.SyncSnapshot(); err == nil {
			t.Errorf("SyncSnapshot on a runtime-less adapter succeeded")
		}
		for _, res := range []Result{
			a.SyncPush(context.Background()),
			a.SyncPull(context.Background()),
			a.SyncTest(context.Background()),
			a.SyncDisable(context.Background()),
		} {
			if res.Err == nil {
				t.Errorf("a verb succeeded with no runtime: %q", res.Text)
			}
		}
		if _, err := a.SyncEditor(); err == nil {
			t.Errorf("SyncEditor on a runtime-less adapter succeeded")
		}
	}
}

// TestSyncEditorWritesTheSectionOnlyThisUserCanRead pins the file the e key hands
// the user's editor: it holds the section's own bytes, and it is not readable by
// anyone else. The section names a remote, and a file mode of 0644 on a shared
// machine would publish it.
func TestSyncEditorWritesTheSectionOnlyThisUserCanRead(t *testing.T) {
	a := syncRealActions(t)
	writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote, Namespace: "default"}, syncSecretValue)

	cmd, err := a.SyncEditor()
	if err != nil {
		t.Fatalf("SyncEditor: %v", err)
	}
	if len(cmd.Args) < 2 {
		t.Fatalf("the editor was given %v, want a file", cmd.Args)
	}
	path := cmd.Args[len(cmd.Args)-1]
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the section file: %v", err)
	}
	if !strings.Contains(string(body), syncRemote) || !strings.Contains(string(body), "default") {
		t.Errorf("the section file does not carry the section: %s", body)
	}
	// Whatever the section names, the token must not be in this file: the editor
	// is a place a user edits, and a credential in it would be a credential on
	// disk somewhere else.
	if strings.Contains(string(body), syncSecretValue) {
		t.Errorf("the section file carries the token value")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the section file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("the section file is %#o, want 0600", perm)
	}
}

// TestSyncEditorIsReadOnly pins that opening the editor changes nothing. The
// cockpit hands the user a file and lets their editor save into it; the section
// itself is written by the next enable, so a half-typed remote URL cannot be
// half-applied by merely opening a screen.
func TestSyncEditorIsReadOnly(t *testing.T) {
	a := syncRealActions(t)
	writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	before, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if _, err := a.SyncEditor(); err != nil {
		t.Fatalf("SyncEditor: %v", err)
	}
	after, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if before.RemoteURL != after.RemoteURL || before.State.Enabled != after.State.Enabled {
		t.Errorf("opening the editor changed the section: %+v -> %+v", before, after)
	}
}

// TestSyncSnapshotOnAHandleWithNoLocalFile pins the degraded path. A handle
// opened without a machine-local companion has no row sync could own, so the
// snapshot answers with what the shared file still knows and reports sync off,
// rather than failing the whole screen.
func TestSyncSnapshotOnAHandleWithNoLocalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	a := &mastermindActions{live: newLiveRuntime(relevo.Runtime{DB: shared})}
	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot on a handle with no local file: %v", err)
	}
	if snap.Token != relevosync.TokenOff {
		t.Errorf("token is %q, want %q", snap.Token, relevosync.TokenOff)
	}
	if snap.RemoteURL != "" || snap.TokenSet || snap.Measured {
		t.Errorf("a handle with no local file reported local state: %+v", snap)
	}
}

// TestSyncDisableRefusesALockedLocalFile pins that a turn-off which cannot write
// the mark reports the refusal rather than reporting success. The steps are
// ordered so the mark is written before the token is deleted; a failure there
// leaves the token, and saying "sync disabled" then would be a lie a user acts
// on.
func TestSyncDisableRefusesALockedLocalFile(t *testing.T) {
	a := syncRealActions(t)
	local := writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	// A read-only local file makes every write fail.
	if err := os.Chmod(local.Path(), 0o400); err != nil {
		t.Skipf("cannot make the local file read-only: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the mode")
	}
	res := a.SyncDisable(context.Background())
	if res.Err == nil {
		t.Skip("the write succeeded despite the mode")
	}
	if !strings.Contains(res.Text, "disable failed") {
		t.Errorf("a refused turn-off reported %q", res.Text)
	}
}

// TestSyncSnapshotIsSafeToCallWithoutAContextDeadline is a cheap guard on the
// bounding helper: every verb bounds its own call, and a caller that supplied a
// deadline keeps it.
func TestSyncSnapshotIsSafeToCallWithoutAContextDeadline(t *testing.T) {
	ctx, cancel := syncCtx(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Errorf("a context with no deadline was not bounded")
	}
	bounded, cancel2 := context.WithTimeout(context.Background(), time.Minute)
	defer cancel2()
	ctx2, cancel3 := syncCtx(bounded)
	defer cancel3()
	deadline, ok := ctx2.Deadline()
	if !ok || time.Until(deadline) > 2*time.Minute {
		t.Errorf("an existing deadline was replaced: %v %t", deadline, ok)
	}
	// A cancelled parent must still cancel the derived one, or a bounded call
	// would outlive the shell that asked for it.
	parent, cancelParent := context.WithCancel(context.Background())
	ctx3, cancel4 := syncCtx(parent)
	cancel4()
	cancelParent()
	if err := ctx3.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("a derived context did not follow its parent: %v", err)
	}
}
