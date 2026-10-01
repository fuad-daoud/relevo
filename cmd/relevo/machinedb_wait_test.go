//go:build unix

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/store"
)

// boundOwner is an owner that has opened the database and bound the root's
// socket, but is not serving yet: the order the daemon itself uses, where the
// listener exists for a while before anyone accepts on it.
type boundOwner struct {
	db *db.DB
	ln *countingListener
}

// bindOwner opens the database under stateHome and binds its socket, the way
// the daemon does before it serves. It reports an error instead of failing the
// test, so a goroutine can call it.
func bindOwner(stateHome string) (*boundOwner, error) {
	root := filepath.Join(stateHome, "relevo")
	d, err := openDBDirect(filepath.Join(root, "relevo.db"))
	if err != nil {
		return nil, fmt.Errorf("openDBDirect: %w", err)
	}
	if err := checkSocketPath(root); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("checkSocketPath: %w", err)
	}
	raw, err := openOwnerListener(root)
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("openOwnerListener: %w", err)
	}
	return &boundOwner{db: d, ln: &countingListener{Listener: raw}}, nil
}

// bindTestOwner is bindOwner for a test goroutine: it fails the test on error
// and cleans up the database and the listener.
func bindTestOwner(t *testing.T, stateHome string) *boundOwner {
	t.Helper()
	b, err := bindOwner(stateHome)
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { _ = b.ln.Close() })
	t.Cleanup(func() { _ = b.db.Close() })
	return b
}

// serve starts accepting on the bound socket, the way the daemon does.
func (b *boundOwner) serve(t *testing.T) *owner.Server {
	t.Helper()
	srv, err := serveOwner(b.db, b.ln)
	if err != nil {
		t.Fatalf("serveOwner: %v", err)
	}
	if srv == nil {
		t.Fatal("serveOwner returned no owner")
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// serveAfter starts accepting on the bound socket delay from now, from a
// goroutine: the "bound but not serving" owner a wait test needs.
func (b *boundOwner) serveAfter(t *testing.T, delay time.Duration) {
	t.Helper()
	var (
		mu  sync.Mutex
		srv *owner.Server
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(delay)
		s, err := serveOwner(b.db, b.ln)
		if err != nil {
			t.Errorf("serveOwner: %v", err)
			return
		}
		mu.Lock()
		srv = s
		mu.Unlock()
	}()
	t.Cleanup(func() {
		<-done
		mu.Lock()
		defer mu.Unlock()
		if srv != nil {
			_ = srv.Close()
		}
	})
}

// ownerLater binds and serves stateHome's socket delay from now, from a
// goroutine: the from-scratch start as a client that arrived first sees it.
// The cleanup waits for the goroutine and reports what it could not do.
func ownerLater(t *testing.T, stateHome string, delay time.Duration) {
	t.Helper()
	var (
		mu      sync.Mutex
		started *testOwner
		berr    error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(delay)
		b, err := bindOwner(stateHome)
		if err != nil {
			berr = err
			return
		}
		srv, err := serveOwner(b.db, b.ln)
		if err != nil {
			berr = fmt.Errorf("serveOwner: %w", err)
			_ = b.ln.Close()
			_ = b.db.Close()
			return
		}
		mu.Lock()
		started = &testOwner{db: b.db, ln: b.ln, srv: srv}
		mu.Unlock()
	}()
	t.Cleanup(func() {
		<-done
		mu.Lock()
		defer mu.Unlock()
		if berr != nil {
			t.Errorf("late owner: %v", berr)
			return
		}
		if started != nil {
			_ = started.srv.Close()
			_ = started.ln.Close()
			_ = started.db.Close()
		}
	})
}

// startStaleSocket leaves a socket file behind with nobody bound to it: an
// image that bound and closed, or a start that died between bind and serve.
func startStaleSocket(t *testing.T, sock string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(sock), err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	l.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("the stale socket is gone: %v", err)
	}
}

// TestVerbWaitsForAnOwnerBoundButNotServing pins the wait itself: a verb whose
// first attempt found a listener that never answered waits for the owner to
// start serving, then reads through the owner's handle. One waiting line goes
// to stderr and stdout stays clean.
func TestVerbWaitsForAnOwnerBoundButNotServing(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)
	withWaitingOwnerRoute(t)

	bindTestOwner(t, root).serveAfter(t, 2500*time.Millisecond)

	var d *db.DB
	start := time.Now()
	stdout, stderr, err := captureOutput(t, func() error {
		var oerr error
		d, oerr = openDB(machineDBPath())
		return oerr
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("openDB with an owner bound but not serving: %v (stderr %q)", err, stderr)
	}
	t.Cleanup(func() { _ = d.Close() })

	if elapsed <= verbDialBudget {
		t.Errorf("openDB took %v, want more than the %v dial budget", elapsed, verbDialBudget)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
	if got := strings.Count(string(stderr), "the relevo daemon is starting"); got != 1 {
		t.Errorf("waiting lines on stderr = %d (%q), want exactly 1", got, stderr)
	}
	if _, ok, kerr := d.KVGet("waiting"); kerr != nil || ok {
		t.Errorf("KVGet after the wait = ok %v, err %v; want an empty read", ok, kerr)
	}
}

// TestVerbWaitsWhileTheDaemonLockIsHeld pins the other evidence: with no socket
// yet, the daemon lock is what says a start is under way, and the verb waits
// for the socket the daemon binds later.
func TestVerbWaitsWhileTheDaemonLockIsHeld(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)
	withWaitingOwnerRoute(t)

	lock, err := store.New(machineRoot(t)).AcquireDaemonLock()
	if err != nil {
		t.Fatalf("AcquireDaemonLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	ownerLater(t, root, 2500*time.Millisecond)

	var d *db.DB
	start := time.Now()
	stdout, stderr, err := captureOutput(t, func() error {
		var oerr error
		d, oerr = openDB(machineDBPath())
		return oerr
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("openDB with the lock held and an owner still to come: %v (stderr %q)", err, stderr)
	}
	t.Cleanup(func() { _ = d.Close() })

	if elapsed <= verbDialBudget {
		t.Errorf("openDB took %v, want more than the %v dial budget", elapsed, verbDialBudget)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
	if _, ok, kerr := d.KVGet("waiting"); kerr != nil || ok {
		t.Errorf("KVGet after the wait = ok %v, err %v; want an empty read", ok, kerr)
	}
}

// TestVerbWithNoDaemonStillFailsFast pins that the wait is gated on evidence: a
// missing socket with no lock is nobody starting, so the verb spends only its
// dial budget, names the owner unavailable, and prints no waiting line.
func TestVerbWithNoDaemonStillFailsFast(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)
	withWaitingOwnerRoute(t)

	start := time.Now()
	stdout, stderr, err := captureOutput(t, func() error {
		_, oerr := openDB(machineDBPath())
		return oerr
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("openDB with no daemon succeeded")
	}
	if !errors.Is(err, errOwnerUnavailable) {
		t.Errorf("openDB error = %v, want it to wrap errOwnerUnavailable", err)
	}
	if elapsed > verbDialBudget+time.Second {
		t.Errorf("openDB took %v, want at most the %v dial budget plus slack", elapsed, verbDialBudget)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
	if strings.Contains(string(stderr), "the relevo daemon is starting") {
		t.Errorf("stderr = %q, want no waiting line", stderr)
	}
}

// TestStaleSocketDoesNotWait pins the other side of the evidence: a socket file
// with nobody bound and no lock is a dead end, not a start, so the verb fails
// on its dial budget and prints nothing extra.
func TestStaleSocketDoesNotWait(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)
	withWaitingOwnerRoute(t)
	startStaleSocket(t, machineSocketPath(t))

	start := time.Now()
	stdout, stderr, err := captureOutput(t, func() error {
		_, oerr := openDB(machineDBPath())
		return oerr
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("openDB with a stale socket succeeded")
	}
	if elapsed > verbDialBudget+time.Second {
		t.Errorf("openDB took %v, want at most the %v dial budget plus slack", elapsed, verbDialBudget)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
	if strings.Contains(string(stderr), "the relevo daemon is starting") {
		t.Errorf("stderr = %q, want no waiting line", stderr)
	}
}

// TestWedgedOwnerFailsAtTheStartWait pins the limit: an owner that holds the
// listener and never answers costs the whole start wait and then refuses with
// an error that names the daemon as starting or unresponsive and names the
// socket. The limit is lowered here so the same path runs in seconds.
func TestWedgedOwnerFailsAtTheStartWait(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	noDaemon(t)
	withWaitingOwnerRoute(t)
	const limit = 3 * time.Second
	dbRouteStartWait = limit
	t.Cleanup(func() { dbRouteStartWait = ownerStartWait })

	bindTestOwner(t, root)

	start := time.Now()
	stdout, stderr, err := captureOutput(t, func() error {
		_, oerr := openDB(machineDBPath())
		return oerr
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("openDB with a wedged owner succeeded")
	}
	if !errors.Is(err, errOwnerUnavailable) {
		t.Errorf("openDB error = %v, want it to wrap errOwnerUnavailable", err)
	}
	for _, want := range []string{"starting or unresponsive", machineSocketPath(t), "after 3s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("openDB error %q must contain %q", err, want)
		}
	}
	if elapsed > limit+time.Second {
		t.Errorf("openDB took %v, want at most %v plus slack", elapsed, limit)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
	if !strings.Contains(string(stderr), "waiting up to 3s") {
		t.Errorf("stderr = %q, want the waiting line naming the limit", stderr)
	}
}

// TestStatuslineKeepsItsShortBudgetWhileAnOwnerStarts pins the exemption: the
// statusline's route carries no start wait, so an owner that is bound and never
// serves costs it only its own short budget, silently, and it exits 0.
func TestStatuslineKeepsItsShortBudgetWhileAnOwnerStarts(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", root)
	noDaemon(t)

	mode, budget, startWait := routeForArgs([]string{"status", "--line"})
	installDBRoute(mode, budget, startWait)
	t.Cleanup(func() { installDBRoute(routeNone, verbDialBudget, 0) })

	bindTestOwner(t, root)

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	origStdin := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() {
		os.Stdin = origStdin
		_ = devnull.Close()
	})

	start := time.Now()
	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"status", "--line"}) })
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("status --line: %v", err)
	}
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Errorf("stdout = %q, stderr = %q; want both empty", stdout, stderr)
	}
	if elapsed > time.Second {
		t.Errorf("status --line took %v, want under a second", elapsed)
	}
}

// TestRouteStartWaitExemptions pins which command lines carry a start wait: the
// statusline, the hooks, the peek verbs and the daemon carry none, and every
// other verb carries the full one.
func TestRouteStartWaitExemptions(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		mode      dbRoute
		budget    time.Duration
		startWait time.Duration
	}{
		{"statusline", []string{"status", "--line"}, routeOwner, statuslineDialBudget, 0},
		{"mastermind init hook", []string{"mastermind", "init", "--hook", "claude"}, routeOwner, verbDialBudget, 0},
		{"mastermind notice hook", []string{"mastermind", "notice", "--hook", "claude"}, routeOwner, verbDialBudget, 0},
		{"config agents", []string{"config", "agents", "--force"}, routeOwner, verbDialBudget, ownerStartWait},
		{"daemon", []string{"daemon"}, routeNone, verbDialBudget, 0},
		{"daemon check", []string{"daemon", "--check"}, routeNone, verbDialBudget, 0},
		{"bugreport", []string{"bugreport"}, routeNone, verbDialBudget, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, budget, startWait := routeForArgs(tc.args)
			if mode != tc.mode || budget != tc.budget || startWait != tc.startWait {
				t.Errorf("routeForArgs(%q) = %v, %v, %v; want %v, %v, %v",
					tc.args, mode, budget, startWait, tc.mode, tc.budget, tc.startWait)
			}
		})
	}
}
