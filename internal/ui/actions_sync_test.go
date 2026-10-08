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

// TestSyncSnapshotCarriesTheExchangeTimesAndItsAbsence pins the pair the
// post-on block reads: whether a completed attempt has ever stamped the
// exchange times, and the times themselves. A machine that has measured nothing
// must not be drawn as a machine with nothing to send.
func TestSyncSnapshotCarriesTheExchangeTimesAndItsAbsence(t *testing.T) {
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
	if err := local.KVPut(relevosync.KeyTimes, []byte(`{"export":"2026-10-04T12:00:00Z","import":"2026-10-04T12:01:00Z"}`)); err != nil {
		t.Fatalf("write the times marker: %v", err)
	}
	snap, err = a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot after the markers: %v", err)
	}
	if !snap.Measured || snap.State.Backlog != 7 || snap.LastExport.IsZero() {
		t.Errorf("the exchange times did not reach the view: %+v", snap)
	}
}

// TestSyncSnapshotCarriesTheLatchBacklogTimesAndTrouble pins that the read side
// of ':sync' carries the state a human acts on: a latched breaker and its
// cause, the backlog, the last exchange times and the import's trouble. A view
// cannot draw what the snapshot never carried.
func TestSyncSnapshotCarriesTheLatchBacklogTimesAndTrouble(t *testing.T) {
	a := syncRealActions(t)
	local := writeSyncSettings(t, a, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	if err := local.KVPut(relevosync.KeyAttention, []byte(`{"at":"2026-10-04T12:00:00Z","message":"sync: the worker stopped answering three times in a row"}`)); err != nil {
		t.Fatalf("write the latch marker: %v", err)
	}
	if err := local.KVPut(relevosync.KeyBacklog, []byte("11")); err != nil {
		t.Fatalf("write the backlog marker: %v", err)
	}
	if err := local.KVPut(relevosync.KeyTimes, []byte(`{"export":"2026-10-04T12:00:00Z","import":"2026-10-04T12:01:00Z"}`)); err != nil {
		t.Fatalf("write the times marker: %v", err)
	}
	if err := local.KVPut(relevosync.KeyTrouble, []byte(`{"held":["m2 is held at 3"]}`)); err != nil {
		t.Fatalf("write the trouble marker: %v", err)
	}

	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.Token != relevosync.TokenErr {
		t.Errorf("token = %q, want %q", snap.Token, relevosync.TokenErr)
	}
	if !snap.State.Attention || snap.State.LatchCause != "sync: the worker stopped answering three times in a row" {
		t.Errorf("the latch did not reach the snapshot: %+v", snap.State)
	}
	if snap.State.Backlog != 11 {
		t.Errorf("backlog = %d, want 11", snap.State.Backlog)
	}
	if snap.LastExport.IsZero() || snap.LastImport.IsZero() {
		t.Errorf("the exchange times did not reach the snapshot: %v %v", snap.LastExport, snap.LastImport)
	}
	if len(snap.Trouble.Held) != 1 {
		t.Errorf("the held origin did not reach the snapshot: %+v", snap.Trouble)
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
