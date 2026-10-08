//go:build unix

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// Every cockpit sync action is a client of the daemon: it sends its verb over
// the owner socket and the daemon runs it with the handles it already holds.
// These tests drive the real adapter over the real owner protocol against a
// real split pair, so the value on the screen -- and the verb that moved it --
// is the one a running daemon would serve, and a cockpit that built its own
// worker would record no verb here at all.

// syncOwnerActions opens a split pair and serves it on a socket short enough
// for sun_path, installing the production-shaped verb hook. It returns the real
// Actions over a dialled handle -- the shape the cockpit has when the daemon is
// running.
func syncOwnerActions(t *testing.T) (*mastermindActions, *db.DB) {
	t.Helper()
	return syncOwnerActionsWithHook(t, nil)
}

// syncOwnerActionsWithHook is syncOwnerActions over a caller's hook, or the
// production-shaped runner when hook is nil. A test that wants to see which
// verb arrived installs a recording hook; every other test uses the runner.
func syncOwnerActionsWithHook(t *testing.T, hook func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult) (*mastermindActions, *db.DB) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")

	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	srv := db.NewOwner(shared)
	if hook == nil {
		hook = newOwnerVerbRunner(t, shared).OwnerVerb
	}
	srv.OnSyncVerb = hook
	go func() { _ = srv.Serve(ln) }()

	dialled, err := db.Dial(sock)
	if err != nil {
		_ = srv.Close()
		_ = ln.Close()
		t.Fatalf("db.Dial: %v", err)
	}
	t.Cleanup(func() {
		_ = dialled.Close()
		_ = srv.Close()
		_ = ln.Close()
		_ = shared.Close()
	})
	if dialled.Local() == nil {
		t.Fatal("the dialled handle carries no machine-local file, so this test cannot reach the post-on block at all")
	}
	return &mastermindActions{live: newLiveRuntime(relevo.Runtime{DB: dialled})}, dialled
}

// newOwnerVerbRunner is the daemon-shaped executor the served owner installs: a
// VerbRunner over the same handles, whose opener refuses on the machine-local
// rows exactly as the supervisor opener does. A machine with a remote and a
// token reaches the generic error below rather than a worker, because no ui
// test may spawn one; a machine missing either refuses with the same sentence
// the real daemon gives.
func newOwnerVerbRunner(t *testing.T, shared *db.DB) *relevo.VerbRunner {
	t.Helper()
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return &relevo.VerbRunner{
		Shared:      shared,
		Local:       local,
		Path:        shared.Path(),
		ReplicaPath: filepath.Join(filepath.Dir(shared.Path()), "relevo-sync.db"),
		ClientName:  "relevo",
		Open: func(context.Context) (synclog.LogTransport, error) {
			settings, err := relevosync.ReadSettings(local)
			if err != nil {
				return nil, err
			}
			if settings.RemoteURL == "" {
				return nil, relevosync.ErrNoRemote
			}
			token, ok, err := relevosync.ReadToken(local)
			if err != nil {
				return nil, err
			}
			if !ok || len(bytes.TrimSpace(token)) == 0 {
				return nil, relevosync.ErrNoToken
			}
			return nil, fmt.Errorf("ui test: no worker is built")
		},
	}
}

// writeOwnerSyncSettings seeds the local file the way an enable and a measured
// tick would, writing through the direct handle so the values are on disk before
// the owner is asked for them.
func writeOwnerSyncSettings(t *testing.T, dialled *db.DB, st relevosync.Settings, token string) {
	t.Helper()
	local := dialled.Local()
	body, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("encode the section: %v", err)
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
	if err := local.KVPut(relevosync.KeyLastTick, []byte(`{"at":"2026-10-04T12:00:00Z","ok":true}`)); err != nil {
		t.Fatalf("write the tick marker: %v", err)
	}
}

// TestCockpitActionsReachTheOwnerVerbHook is the pin for the one writer: every
// cockpit action sends its verb to the daemon's hook rather than building a
// sync worker of its own. A local runner would never record a verb here, and a
// second worker on the replica the daemon's worker holds is the failure this
// route removes.
func TestCockpitActionsReachTheOwnerVerbHook(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	hook := func(_ context.Context, v *wire.SyncVerb, _ []byte) *wire.SyncResult {
		mu.Lock()
		seen = append(seen, v.Verb)
		mu.Unlock()
		return &wire.SyncResult{OK: true, Applied: true}
	}
	a, _ := syncOwnerActionsWithHook(t, hook)

	for _, call := range []func(context.Context) Result{
		a.SyncPush, a.SyncPull, a.SyncTest, a.SyncDisable,
	} {
		if res := call(context.Background()); res.Err != nil {
			t.Fatalf("an action refused over the owner hook: %v (%s)", res.Err, res.Text)
		}
	}
	want := []string{wire.SyncVerbPush, wire.SyncVerbPull, wire.SyncVerbProbe, wire.SyncVerbDisable}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("the owner saw %v, want %v", seen, want)
	}
}

// TestSyncActionsRefuseWithoutARemoteOrToken pins the refusal every action that
// would reach a remote gives on a machine that has no remote, no token, or
// neither. The daemon's opener refuses on the machine-local rows before a
// worker is built, and the refusal crosses the socket to the cockpit with the
// sentence that names the missing row.
func TestSyncActionsRefuseWithoutARemoteOrToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dialled *db.DB)
	}{
		{"neither a remote nor a token", func(t *testing.T, dialled *db.DB) {}},
		{"a token and no remote", func(t *testing.T, dialled *db.DB) {
			writeOwnerSyncSettings(t, dialled, relevosync.Settings{Enabled: true}, syncSecretValue)
		}},
		{"a remote and no token", func(t *testing.T, dialled *db.DB) {
			writeOwnerSyncSettings(t, dialled, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, dialled := syncOwnerActions(t)
			tc.setup(t, dialled)
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

// TestSyncSnapshotOverTheOwnerRouteShowsRealValues is the post-on capture over
// the real adapter, a real socket and a real split pair, with the markers an
// enable and a measured attempt leave behind. Every value the post-on block
// draws has to come back as itself -- the remote, the backlog, the stamps --
// because the alternative is a screen that says a configured machine has
// nothing configured.
func TestSyncSnapshotOverTheOwnerRouteShowsRealValues(t *testing.T) {
	a, dialled := syncOwnerActions(t)
	writeOwnerSyncSettings(t, dialled, relevosync.Settings{Enabled: true, RemoteURL: syncRemote, Namespace: "default"}, syncSecretValue)
	local := dialled.Local()
	if err := local.KVPut(relevosync.KeyBacklog, []byte("3")); err != nil {
		t.Fatalf("write the backlog marker: %v", err)
	}
	if err := local.KVPut(relevosync.KeyTimes, []byte(`{"export":"2026-10-04T12:00:00Z","import":"2026-10-04T12:01:00Z"}`)); err != nil {
		t.Fatalf("write the times marker: %v", err)
	}

	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot over the owner route: %v", err)
	}
	if snap.RemoteURL != syncRemote {
		t.Errorf("remote = %q, want %q: the local section did not reach the cockpit over the dial", snap.RemoteURL, syncRemote)
	}
	if !snap.TokenSet {
		t.Error("the stored token is not reported present over the owner route")
	}
	if snap.Token != relevosync.TokenOK {
		t.Errorf("token = %q, want %q", snap.Token, relevosync.TokenOK)
	}
	if !snap.State.Enabled || !snap.Measured {
		t.Errorf("state over the owner route = %+v, measured %t", snap.State, snap.Measured)
	}
	if snap.State.Backlog != 3 {
		t.Errorf("backlog = %d, want 3", snap.State.Backlog)
	}
	if snap.LastExport.IsZero() || snap.LastImport.IsZero() {
		t.Errorf("the exchange times did not reach the cockpit: %v %v", snap.LastExport, snap.LastImport)
	}

	// The rendered body, not just the snapshot: the screen is what a user reads,
	// so the real values have to be on it and the placeholders must not be.
	v := syncView{snap: snap, loaded: true, actions: true, now: syncNow}
	joined := strings.Join(v.bodyLines(132), "\n")
	for _, want := range []string{
		syncRemote,
		"3 operations",
		relevosync.TokenOK,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the post-on body does not carry %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"not configured", "not measured", "never"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("the post-on body still says %q on a machine that has measured:\n%s", unwanted, joined)
		}
	}
	if got := v.SyncToken(); got != relevosync.TokenOK {
		t.Errorf("the view's own token = %q, want %q", got, relevosync.TokenOK)
	}
}

// TestSyncDisableOverTheOwnerRouteTurnsSyncOff pins the write side of the same
// route: a turn-off issued from a handle that reached the database through the
// owner has to land in that machine's local file. A disable that removed nothing
// would report success and leave sync on, which is the one outcome the verb
// exists to prevent.
func TestSyncDisableOverTheOwnerRouteTurnsSyncOff(t *testing.T) {
	a, dialled := syncOwnerActions(t)
	writeOwnerSyncSettings(t, dialled, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	res := a.SyncDisable(context.Background())
	if res.Err != nil {
		t.Fatalf("SyncDisable over the owner route: %v (%s)", res.Err, res.Text)
	}
	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.State.Enabled || snap.Token != relevosync.TokenOff {
		t.Errorf("sync is still on after the turn-off over the owner route: %+v", snap)
	}
	if snap.TokenSet {
		t.Error("the token survived the turn-off over the owner route")
	}
}

// TestSyncDisableClearsTheJoinMarker pins that a turn-off takes the join marker
// with the mark: a machine that is off is not joining, and a marker left behind
// would have status show a join on a machine that is not moving.
func TestSyncDisableClearsTheJoinMarker(t *testing.T) {
	a, dialled := syncOwnerActions(t)
	writeOwnerSyncSettings(t, dialled, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)
	local := dialled.Local()
	if err := relevosync.WriteJoin(local, syncNow); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	if _, ok, err := relevosync.ReadJoin(local); err != nil || !ok {
		t.Fatalf("the join marker was not written: (ok %v, err %v)", ok, err)
	}

	if res := a.SyncDisable(context.Background()); res.Err != nil {
		t.Fatalf("SyncDisable: %v (%s)", res.Err, res.Text)
	}
	if _, ok, err := relevosync.ReadJoin(local); err != nil || ok {
		t.Errorf("the turn-off left the join marker behind: (ok %v, err %v)", ok, err)
	}
}

// TestSyncDisableWithoutSyncIsNotAnError pins that turning sync off on a machine
// that never had it on still succeeds. A turn-off is idempotent, and refusing it
// would leave a user with no way to clear a section they want gone.
func TestSyncDisableWithoutSyncIsNotAnError(t *testing.T) {
	a, _ := syncOwnerActions(t)
	res := a.SyncDisable(context.Background())
	if res.Err != nil {
		t.Fatalf("SyncDisable on a machine that never enabled: %v", res.Err)
	}
}

// TestSyncOverTheOwnerRouteStillReadsOnlyTheLocalFile pins the split over the
// wire: a section written into the shared file must not answer for the local
// one. A read that fell back to the shared file would report a remote no enable
// ever set, on a machine whose real remote is something else.
func TestSyncOverTheOwnerRouteStillReadsOnlyTheLocalFile(t *testing.T) {
	a, dialled := syncOwnerActions(t)
	writeOwnerSyncSettings(t, dialled, relevosync.Settings{Enabled: true, RemoteURL: syncRemote}, syncSecretValue)

	if err := dialled.Tx(func(tx *db.Tx) error {
		return tx.ConfigPut(relevosync.SectionSettings, []byte(`{"enabled":true,"remote_url":"libsql://elsewhere.turso.io"}`), syncNow)
	}); err != nil {
		t.Fatalf("write a shared section: %v", err)
	}
	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot: %v", err)
	}
	if snap.RemoteURL != syncRemote {
		t.Errorf("the read took the shared section over the owner route: %q", snap.RemoteURL)
	}
}

// TestSyncSnapshotOverAnOwnerWithoutALocalFileStaysOff pins the degraded case on
// the dial: an owner serving only the shared file leaves the cockpit with
// nothing sync could own, and the screen must say so rather than fail or invent
// a remote.
func TestSyncSnapshotOverAnOwnerWithoutALocalFileStaysOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := db.NewOwner(shared)
	go func() { _ = srv.Serve(ln) }()
	dialled, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("db.Dial: %v", err)
	}
	t.Cleanup(func() {
		_ = dialled.Close()
		_ = srv.Close()
		_ = ln.Close()
	})

	a := &mastermindActions{live: newLiveRuntime(relevo.Runtime{DB: dialled})}
	snap, err := a.SyncSnapshot()
	if err != nil {
		t.Fatalf("SyncSnapshot on an owner with no local file: %v", err)
	}
	if snap.Token != relevosync.TokenOff || snap.RemoteURL != "" || snap.TokenSet || snap.Measured {
		t.Errorf("an owner with no local file reported local state: %+v", snap)
	}
}
