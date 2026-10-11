//go:build unix

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// startupRuntime binds a split pair in the state root the environment selects
// and builds the runtime the daemon builds over it, so these tests drive the
// real startup order against a real owner socket rather than a stand-in for
// either. The home and config roots are moved inside the state root so the
// runtime's config import reads the fixture rather than the caller's.
func startupRuntime(t *testing.T, stateHome string) (relevo.Runtime, *boundOwner) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateHome, "config"))
	t.Setenv("HOME", stateHome)
	root := filepath.Join(stateHome, "relevo")
	b := bindTestOwner(t, stateHome)
	rt, err := newRuntimeOn(root, b.db)
	if err != nil {
		t.Fatalf("newRuntimeOn: %v", err)
	}
	rt.StartedAt = time.Now()
	return rt, b
}

// stalledPass is the window a stand-in pass holds the startup open for: past
// the whole start wait, so a client that succeeds cannot be mistaken for one
// that waited the owner out. The hold is bounded, so a build that serves after
// the passes fails these tests instead of hanging them.
const stalledPass = ownerStartWait + 5*time.Second

// standInPasses replaces the real passes for the rest of the test and returns
// the two ends of the window they hold: inside blocks until the stand-in pass is
// running and holding the write lock, and finish lets it return.
//
// The pass stays inside for as long as the test holds it there, which is the
// point: a startup that serves after its passes is not serving at all while the
// pass is inside, and a client that arrives in that window is the one this
// change is about. The write lock it holds is the shape of the bug too -- the
// passes serialize on the one pool the owner serves.
func standInPasses(t *testing.T, d *db.DB, rowsMayLeave bool) (inside func(), finish func()) {
	t.Helper()
	prev := daemonStartupPasses
	holding := make(chan struct{})
	running := make(chan struct{})
	release := make(chan struct{})
	settled := make(chan struct{})
	daemonStartupPasses = func(*relevo.Runtime) bool {
		go func() {
			defer close(settled)
			_ = d.Tx(func(*db.Tx) error {
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding
		close(running)
		<-release
		return rowsMayLeave
	}
	var once sync.Once
	inside = func() {
		t.Helper()
		select {
		case <-running:
		case <-time.After(stalledPass):
			t.Fatal("the stand-in pass never reached the write lock")
		}
	}
	finish = func() {
		t.Helper()
		once.Do(func() { close(release) })
		select {
		case <-settled:
		case <-time.After(stalledPass):
			t.Error("the stand-in pass never released the write lock")
		}
	}
	t.Cleanup(func() {
		daemonStartupPasses = prev
		once.Do(func() { close(release) })
		<-settled
	})
	return inside, finish
}

// TestStalledStartupPassStillAnswersAClient pins the reorder: an owner served
// before the one-time passes keeps answering while a pass holds the write lock,
// and it keeps answering long after the pass has outlived the whole start wait.
//
// This is the failure the reorder removed. The listener was bound early and
// served late, so a client that connected during the passes met a backlog with
// nobody accepting on it: it spent its start wait and was then told the daemon
// was starting or unresponsive. Served first, the same client is answered on its
// first dial, and its read returns inside one dial budget while the pass is
// still holding the lock -- which a plain read never needed.
//
// It fails if serve moves back after the passes: the client then finds an
// unserved socket, waits the whole start wait while the pass holds, and is
// refused.
func TestStalledStartupPassStillAnswersAClient(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)
	withWaitingOwnerRoute(t)

	rt, b := startupRuntime(t, root)
	inside, finish := standInPasses(t, b.db, true)

	done := make(chan *owner.Server, 1)
	go func() { done <- daemonStartup(&rt, b.db, b.ln, nil) }()
	began := time.Now()
	inside()

	var d *db.DB
	start := time.Now()
	stdout, stderr, err := captureOutput(t, func() error {
		var oerr error
		d, oerr = openDB(machineDBPath())
		return oerr
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("openDB while a startup pass holds the write lock: %v (stderr %q)", err, stderr)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, _, kerr := d.KVGet("route-pin"); kerr != nil {
		t.Errorf("read through the owner while a pass holds the write lock: %v", kerr)
	}
	// The bound is the start wait rather than the dial budget on purpose: what
	// this pins is that the client never spent it, and the waiting line below is
	// what says it did not wait at all. A loaded machine can stretch a dial; it
	// cannot make a client that spent the start wait succeed inside it.
	if elapsed > ownerStartWait {
		t.Errorf("openDB took %v, want at most the %v start wait: the client waited on an owner that was already serving", elapsed, ownerStartWait)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
	if strings.Contains(string(stderr), "the relevo daemon is starting") {
		t.Errorf("stderr = %q, want no waiting line: the owner was already serving", stderr)
	}

	// The pass is still holding after the read, and it outlives the whole start
	// wait: a client that only succeeded because nothing was in its way would
	// have to be lucky about when it arrived.
	if rest := ownerStartWait - time.Since(began); rest > 0 {
		time.Sleep(rest)
	}
	finish()

	srv := <-done
	if srv == nil {
		t.Fatal("daemonStartup served no owner")
	}
	t.Cleanup(func() { _ = srv.Close() })
}

// TestSyncVerbIsRefusedUntilTheStartupPassesFinish pins the other half of the
// order: the socket answers SQL the moment it is served, but the sync verb --
// the one request that pushes rows off this machine -- stays uninstalled until
// the passes have recorded themselves.
//
// A verb arriving mid-pass is refused with the owner's own no-executor code, so
// a client of any version reads it the way it always has, and the executor is
// there once the passes are done. It fails if the executor is installed with the
// serve, and it fails if it is never installed at all.
func TestSyncVerbIsRefusedUntilTheStartupPassesFinish(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)

	rt, b := startupRuntime(t, root)
	sock := machineSocketPath(t)

	prev := daemonStartupPasses
	var during error
	daemonStartupPasses = func(*relevo.Runtime) bool {
		during = askSyncVerb(t, sock, "mid-pass")
		return true
	}
	t.Cleanup(func() { daemonStartupPasses = prev })

	srv := daemonStartup(&rt, b.db, b.ln, nil)
	if srv == nil {
		t.Fatal("daemonStartup served no owner")
	}
	t.Cleanup(func() { _ = srv.Close() })

	if code, ok := refusalCode(during); !ok || code != wire.RefuseNoSyncVerb {
		t.Errorf("a verb during the passes was refused with %q, want the existing %q (err %v)",
			code, wire.RefuseNoSyncVerb, during)
	}

	after := askSyncVerb(t, sock, "after-passes")
	if code, ok := refusalCode(after); ok && code == wire.RefuseNoSyncVerb {
		t.Errorf("a verb after the passes was still refused with %q: the executor was never installed", code)
	}
}

// TestSyncVerbStaysOffWhenTheEnablePathDidNotFinish pins the gate rather than
// the order: a database whose split or backfill did not record itself keeps the
// verb uninstalled, however long ago the socket started serving.
//
// This is the shape that would otherwise export the very rows the split exists
// to move -- a machine whose secrets are still sitting in the shared file, taken
// by a push and uploaded. Refusing the verb is not refusing the owner, so the
// same socket still answers the reads and writes that need no egress.
func TestSyncVerbStaysOffWhenTheEnablePathDidNotFinish(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)

	rt, b := startupRuntime(t, root)
	sock := machineSocketPath(t)

	prev := daemonStartupPasses
	daemonStartupPasses = func(*relevo.Runtime) bool { return false }
	t.Cleanup(func() { daemonStartupPasses = prev })

	srv := daemonStartup(&rt, b.db, b.ln, nil)
	if srv == nil {
		t.Fatal("daemonStartup served no owner")
	}
	t.Cleanup(func() { _ = srv.Close() })

	d, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("dial the served owner: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, _, kerr := d.KVGet("route-pin"); kerr != nil {
		t.Errorf("read through the owner: %v", kerr)
	}

	err = askSyncVerb(t, sock, "enable-path-incomplete")
	if code, ok := refusalCode(err); !ok || code != wire.RefuseNoSyncVerb {
		t.Errorf("a verb on a database whose enable path did not finish was refused with %q, want the existing %q (err %v)",
			code, wire.RefuseNoSyncVerb, err)
	}
}

// askSyncVerb sends one push verb over the socket and returns what the owner
// answered as an error, the way a client reads it. A push on a machine with no
// remote configured is refused locally, so no test here can reach a network.
func askSyncVerb(t *testing.T, sock, id string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), verbDialBudget)
	defer cancel()
	res, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, nil)
	t.Logf("verb %s answered by %v", id, res)
	return errors.Join(client.VerbError(res), err)
}

// refusalCode is the wire code a verb was refused with, ok false when the verb
// was refused by the transport rather than answered. The owner answers an
// uninstalled verb with a refusal frame rather than a result, so this is the
// same classification dbSyncVerbTransport makes.
func refusalCode(err error) (string, bool) {
	var refused *wire.Refusal
	if errors.As(err, &refused) {
		return refused.Code, true
	}
	var verb *client.VerbRefusal
	if errors.As(err, &verb) {
		return verb.Code, true
	}
	return "", false
}

// TestStartupReclaimsBeforeItServes pins where the reclaim sits. A compaction
// closes and reopens the pool, which a served handle refuses, so a reclaim
// moved after the serve compacts nothing and only warns.
func TestStartupReclaimsBeforeItServes(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)

	rt, b := startupRuntime(t, root)
	prevPasses, prevReclaim := daemonStartupPasses, reclaimFreePages
	t.Cleanup(func() { daemonStartupPasses, reclaimFreePages = prevPasses, prevReclaim })
	daemonStartupPasses = func(*relevo.Runtime) bool { return false }
	called := false
	var vacuumErr error
	reclaimFreePages = func(d *db.DB) ([]db.ReclaimedFile, error) {
		called = true
		vacuumErr = d.Vacuum()
		return nil, vacuumErr
	}

	srv := daemonStartup(&rt, b.db, b.ln, nil)
	if srv != nil {
		t.Cleanup(func() { _ = srv.Close() })
	}
	if !called {
		t.Fatal("the start ran no reclaim")
	}
	if vacuumErr != nil {
		t.Errorf("the reclaim could not compact the handle: %v", vacuumErr)
	}
}
