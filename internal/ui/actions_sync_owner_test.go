//go:build unix

package ui

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The gap this file closes: the :sync adapter had only ever been tested against
// a handle opened directly, and the cockpit's handle is dialled. Every read the
// post-on block draws comes out of the machine-local file, so a route that
// carried no local file reported a machine that had never enabled sync. These
// tests drive the real adapter over the real owner protocol against a real split
// pair, so the value on the screen is the one a running daemon would serve.

// syncOwnerActions opens a split pair, serves it on a socket short enough for
// sun_path, and returns the real Actions over a dialled handle -- the shape the
// cockpit has when the daemon is running.
func syncOwnerActions(t *testing.T) (*mastermindActions, *db.DB) {
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

// TestSyncSnapshotOverTheOwnerRouteShowsRealValues is the post-on capture the
// S5 round could not take: the real adapter, over a real socket, against a real
// split pair, with the markers an enable and a measured tick leave behind. Every
// value the post-on block draws has to come back as itself -- the remote, the
// backlog, the stamps, the byte counts, the revision -- because the alternative
// is a screen that says a configured machine has nothing configured.
func TestSyncSnapshotOverTheOwnerRouteShowsRealValues(t *testing.T) {
	a, dialled := syncOwnerActions(t)
	writeOwnerSyncSettings(t, dialled, relevosync.Settings{Enabled: true, RemoteURL: syncRemote, Namespace: "default"}, syncSecretValue)
	local := dialled.Local()
	if err := local.KVPut(relevosync.KeyBacklog, []byte("3")); err != nil {
		t.Fatalf("write the backlog marker: %v", err)
	}
	stats := relevosync.Stats{
		CdcOperations:        3,
		LastPushUnixTime:     syncNow.Add(-2 * time.Minute).Unix(),
		LastPullUnixTime:     syncNow.Add(-5 * time.Minute).Unix(),
		NetworkSentBytes:     1_284_000,
		NetworkReceivedBytes: 842_000,
		Revision:             "rev-7f3a91c2",
	}
	body, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("encode the snapshot: %v", err)
	}
	if err := local.KVPut(relevosync.KeyStats, body); err != nil {
		t.Fatalf("write the snapshot marker: %v", err)
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
	if snap.Stats != stats {
		t.Errorf("stats over the owner route = %+v, want %+v", snap.Stats, stats)
	}

	// The rendered body, not just the snapshot: the screen is what a user reads,
	// so the real values have to be on it and the placeholders must not be.
	v := syncView{snap: snap, loaded: true, actions: true, now: syncNow}
	joined := strings.Join(v.bodyLines(132), "\n")
	for _, want := range []string{
		syncRemote,
		"3 operations",
		"rev-7f3a91c2",
		"1.3 MB",
		"842.0 kB",
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
	// Push, pull and test are off the footer whatever the section says: this
	// build has no engine behind them, so a machine configured with a remote and
	// a token refuses exactly as a bare one does.
	if got, want := v.OffKeys(Env{Actions: a}), []string{"p", "l", "t"}; len(got) != len(want) {
		t.Errorf("OffKeys = %v, want the three verbs this build refuses", got)
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
