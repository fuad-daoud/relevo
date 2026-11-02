//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// machineRoot resolves the state root the current XDG_STATE_HOME selects.
func machineRoot(t *testing.T) string {
	t.Helper()
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	return root
}

// machineSocketPath is the socket the current environment's owner serves.
func machineSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(machineRoot(t), "relevo.sock")
}

// countingListener records how many connections the owner accepted, so a test
// can prove a direct open touched none.
type countingListener struct {
	net.Listener
	accepts int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	nc, err := l.Listener.Accept()
	if err == nil {
		atomic.AddInt32(&l.accepts, 1)
	}
	return nc, err
}

// testOwner is an in-process owner serving <root>/relevo.sock.
type testOwner struct {
	db  *db.DB
	ln  *countingListener
	srv *owner.Server
}

// startTestOwner opens the machine database directly and serves it on the
// root's socket, the way the daemon does. The bind and the serve are separate
// steps so a wait test can bind early and serve late, the daemon's own order.
func startTestOwner(t *testing.T, stateHome string) *testOwner {
	t.Helper()
	b := bindTestOwner(t, stateHome)
	return &testOwner{db: b.db, ln: b.ln, srv: b.serve(t)}
}

// withOwnerRoute installs the production route for the test and clears it when
// the test ends, so the next test stays direct. Its start wait is 0: these
// tests want the route's dial budget, not the wait, which has its own tests.
func withOwnerRoute(t *testing.T, budget time.Duration) {
	t.Helper()
	installDBRoute(routeOwner, budget, 0)
	t.Cleanup(func() { installDBRoute(routeNone, verbDialBudget, 0) })
}

// withWaitingOwnerRoute installs the production route with the real start wait,
// so a test can watch a verb wait for a daemon that is still starting.
func withWaitingOwnerRoute(t *testing.T) {
	t.Helper()
	installDBRoute(routeOwner, verbDialBudget, ownerStartWait)
	t.Cleanup(func() { installDBRoute(routeNone, verbDialBudget, 0) })
}

// noDaemon replaces the starter with a no-op and restores it afterwards.
func noDaemon(t *testing.T) {
	t.Helper()
	daemonStarter = func() error { return nil }
	t.Cleanup(func() { daemonStarter = startDaemon })
}

// TestOpenDBDialsTheOwnerSocket pins the client opener: the machine path
// reaches the owner's handle and the client mints no installation file.
func TestOpenDBDialsTheOwnerSocket(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	served := startTestOwner(t, root)
	if err := served.db.KVPut("route-pin", []byte(`"owner"`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	// The owner minted this file when it opened the database; a client must
	// not mint it again.
	if err := os.Remove(filepath.Join(machineRoot(t), "installation.json")); err != nil {
		t.Fatalf("remove installation.json: %v", err)
	}
	withOwnerRoute(t, verbDialBudget)

	d, err := openDB(machineDBPath())
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if got, want := d.Route(), "owner "+machineSocketPath(t); got != want {
		t.Errorf("route = %q, want %q", got, want)
	}
	value, ok, err := d.KVGet("route-pin")
	if err != nil || !ok {
		t.Fatalf("KVGet = %q, %v, %v; want the owner's row", value, ok, err)
	}
	if string(value) != `"owner"` {
		t.Errorf("value = %q, want the owner's row", value)
	}
	if _, err := os.Stat(filepath.Join(machineRoot(t), "installation.json")); !os.IsNotExist(err) {
		t.Errorf("the client minted installation.json: stat error = %v, want not-exist", err)
	}
}

// TestRouteIgnoresTheRetiredDirectEnv pins that the RELEVO_DB_DIRECT escape
// hatch is gone: setting it must not change the route.
func TestRouteIgnoresTheRetiredDirectEnv(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("RELEVO_DB_DIRECT", "1")

	mode, budget, startWait := routeForArgs([]string{"status"})
	if mode != routeOwner || budget != verbDialBudget || startWait != ownerStartWait {
		t.Errorf("routeForArgs(status) with RELEVO_DB_DIRECT = %v, %v, %v; want %v, %v, %v",
			mode, budget, startWait, routeOwner, verbDialBudget, ownerStartWait)
	}
}

// TestPeekNeverDialsOrStarts pins the read-only verbs: they install no route,
// start nothing, and leave no database, socket or installation file behind,
// even with an agy environment that would otherwise be captured.
func TestPeekNeverDialsOrStarts(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", root)
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "ses_peek")

	var calls int32
	daemonStarter = func() error { atomic.AddInt32(&calls, 1); return nil }
	t.Cleanup(func() { daemonStarter = startDaemon })

	if mode, _, _ := routeForArgs([]string{"daemon", "--check"}); mode != routeNone {
		t.Errorf("routeForArgs(daemon --check) = %v, want none", mode)
	}

	// bugreport is a read-only verb too: it reads the machine through its own
	// read-only handle, so it gets routeNone and the peek flag -- the exact
	// value run() branches on to skip captureAgyEnv -- whatever its own flags
	// select.
	if !isPeekArgs([]string{"bugreport"}) {
		t.Error("isPeekArgs(bugreport) = false, want true")
	}
	if mode, _, _ := routeForArgs([]string{"bugreport"}); mode != routeNone {
		t.Errorf("routeForArgs(bugreport) = %v, want none", mode)
	}
	if !isPeekArgs([]string{"bugreport", "--name", "alpha", "--round", "2", "--logs"}) {
		t.Error("isPeekArgs(bugreport --name/--round/--logs) = false, want true")
	}
	if mode, _, _ := routeForArgs([]string{"bugreport", "--name", "alpha", "--round", "2", "--logs"}); mode != routeNone {
		t.Errorf("routeForArgs(bugreport with flags) = %v, want none", mode)
	}
	if peek := installRouteForArgs([]string{"bugreport"}); !peek {
		t.Error("installRouteForArgs(bugreport) = false, want the peek that skips capture")
	}

	_, _, err := captureOutput(t, func() error { return run([]string{"daemon", "--check"}) })
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Fatalf("daemon --check with no daemon = %v, want exitCodeErr{1}", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("the peek started the daemon %d times, want 0", got)
	}
	for _, name := range []string{"relevo.db", "relevo.db-wal", "relevo.db-shm", "relevo.sock", "installation.json"} {
		if _, serr := os.Stat(filepath.Join(root, name)); !os.IsNotExist(serr) {
			t.Errorf("%s exists after the peek: stat error = %v, want not-exist", name, serr)
		}
	}
}

// TestNoOwnerStartsTheDaemonOnce pins auto-start: a missing socket starts the
// owner once and the open then succeeds; an owner that already answers starts
// nothing.
func TestNoOwnerStartsTheDaemonOnce(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	withOwnerRoute(t, verbDialBudget)

	var calls int32
	daemonStarter = func() error {
		atomic.AddInt32(&calls, 1)
		startTestOwner(t, root)
		return nil
	}
	t.Cleanup(func() { daemonStarter = startDaemon })

	d, err := openDB(machineDBPath())
	if err != nil {
		t.Fatalf("openDB with a starter: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("starter calls = %d, want exactly 1", got)
	}

	answered := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", answered)
	startTestOwner(t, answered)
	atomic.StoreInt32(&calls, 0)

	d2, err := openDB(machineDBPath())
	if err != nil {
		t.Fatalf("openDB with an answering owner: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("starter calls with an answering owner = %d, want 0", got)
	}
}

// TestVerbWithoutAnOwnerNamesTheSocket pins the refusal line every surface
// prints: one line naming the socket, with stdout clean.
func TestVerbWithoutAnOwnerNamesTheSocket(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", root)
	noDaemon(t)
	withOwnerRoute(t, 200*time.Millisecond)

	var runErr error
	stdout, stderr, _ := captureOutput(t, func() error {
		runErr = run([]string{"status"})
		if runErr == nil {
			t.Error("status with no owner succeeded")
			return nil
		}
		report(os.Stderr, runErr, false)
		return nil
	})
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
	if !strings.Contains(string(stderr), machineSocketPath(t)) {
		t.Errorf("stderr %q must name the socket", stderr)
	}
}

// TestStatuslineGivesUpSilently pins the statusline's owner-unavailable rule:
// nothing on stdout or stderr, exit 0, and well under a second.
func TestStatuslineGivesUpSilently(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", root)
	noDaemon(t)
	withOwnerRoute(t, statuslineDialBudget)

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
	if err != nil {
		t.Fatalf("status --line: %v", err)
	}
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Errorf("stdout = %q, stderr = %q; want both empty", stdout, stderr)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("status --line took %v, want under a second", elapsed)
	}
}

// TestStartDaemonHelpers pins the pure helpers the detached spawn is built
// from; no process is spawned.
func TestStartDaemonHelpers(t *testing.T) {
	if got := daemonArgv("/usr/bin/relevo"); !reflect.DeepEqual(got, []string{"/usr/bin/relevo", "daemon", "--auto-exit-after", daemonAutoExitAfter.String()}) {
		t.Errorf("daemonArgv = %v", got)
	}
	if got, want := daemonLogPath("/tmp/rvo-x"), filepath.Join("/tmp/rvo-x", "daemon.log"); got != want {
		t.Errorf("daemonLogPath = %q, want %q", got, want)
	}
	if attrs := detachAttrs(); attrs == nil || !attrs.Setsid {
		t.Errorf("detachAttrs = %+v, want Setsid", attrs)
	}
}

// TestDaemonUnitDetection pins that the unit and plist probes resolve under the
// environment, so a temporary HOME isolates them.
func TestDaemonUnitDetection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	if relevoUnitInstalled() {
		t.Error("a fresh HOME reports the systemd unit installed")
	}
	if daemonPlistInstalled() {
		t.Error("a fresh HOME reports the launchd plist installed")
	}

	unitDir := filepath.Join(home, "config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", unitDir, err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "relevo.service"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	if !relevoUnitInstalled() {
		t.Error("the systemd unit exists but is not detected")
	}

	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", plistDir, err)
	}
	if err := os.WriteFile(filepath.Join(plistDir, daemonLabel+".plist"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	if !daemonPlistInstalled() {
		t.Error("the launchd plist exists but is not detected")
	}
}

// TestOneOpenerWithAnOwnerRunning is the one-opener proof: a re-exec'd child
// reads through the parent's owner and holds no descriptor on the database.
func TestOneOpenerWithAnOwnerRunning(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc: the child's descriptors cannot be inspected")
	}
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	startTestOwner(t, root)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), cmdTestHelperEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper child: %v: %s", err, out)
	}
	var report helperReport
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("unmarshal helper report %q: %v", out, err)
	}
	if report.Version == 0 {
		t.Errorf("child reported version %d, want the owner's schema", report.Version)
	}
	if len(report.DBFDs) != 0 {
		t.Errorf("the child holds database descriptors: %v", report.DBFDs)
	}
}

// runDaemonHelperChild re-execs the test binary as the daemon-runtime child and
// returns its report. The child answers in TestMain before any isolation, so it
// keeps this test's state root.
func runDaemonHelperChild(t *testing.T) daemonHelperReport {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), cmdTestDaemonHelperEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("daemon helper child: %v: %s", err, out)
	}
	var report daemonHelperReport
	if uerr := json.Unmarshal(out, &report); uerr != nil {
		t.Fatalf("unmarshal daemon helper report %q: %v", out, uerr)
	}
	return report
}

// daemonHelperEnv points the child's state root at a short /tmp root, the way
// every socket-path test does, and returns it.
func daemonHelperEnv(t *testing.T) string {
	t.Helper()
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", root)
	return root
}

// TestDaemonRuntimeOpensTheDatabaseOnce pins the daemon's one handle: the
// runtime's store borrows the handle the daemon opened and serves, so the daemon
// process holds exactly one descriptor on relevo.db and rt.DB is that handle.
func TestDaemonRuntimeOpensTheDatabaseOnce(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc: the daemon's descriptors cannot be inspected")
	}
	daemonHelperEnv(t)

	report := runDaemonHelperChild(t)
	if report.Newer {
		t.Fatalf("the child opened a newer schema: %+v", report)
	}
	if !report.RTDBSet {
		t.Error("rt.DB is nil: the daemon's own ingest is not on its one handle")
	}
	if !report.SameStore {
		t.Error("rt.Store.DB() != rt.DB: the runtime's store did not borrow the daemon's handle")
	}
	if !report.StoreIsOwner {
		t.Error("rt.Store.DB() is not the handle the daemon opened: a second handle was opened")
	}
	if report.DBFDs != 1 {
		t.Errorf("the daemon process holds %d descriptors on relevo.db, want exactly 1", report.DBFDs)
	}
}

// TestDaemonServesANewerSchema pins the branch the daemon's one handle makes
// reachable: a database a newer relevo wrote builds the daemon runtime with no
// error, the owner keeps the handle to serve, and the daemon's own ingest is
// paused (rt.DB nil). Before the one handle, newRuntime refused such a file
// through the store and the daemon exited "schema is newer than this relevo".
func TestDaemonServesANewerSchema(t *testing.T) {
	root := daemonHelperEnv(t)
	seedNewerSchema(t, filepath.Join(root, "relevo", "relevo.db"))

	report := runDaemonHelperChild(t)
	if !report.Newer {
		t.Fatalf("the child did not see a newer schema: %+v", report)
	}
	if report.RTDBSet {
		t.Error("rt.DB is set on a newer schema: the daemon's own ingest must be paused")
	}
	if !report.StoreIsOwner {
		t.Error("the owner handle was not kept: the daemon must still serve the newer database")
	}
	if _, err := os.Stat("/proc/self/fd"); err == nil && report.DBFDs != 1 {
		t.Errorf("the daemon process holds %d descriptors on relevo.db, want exactly 1", report.DBFDs)
	}
}

// seedNewerSchema writes a schema_version row above every embedded migration:
// the file an older relevo must serve but never write.
func seedNewerSchema(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	sqlDB := dbtest.RawOpen(t, path)

	if _, err := sqlDB.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (999, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert version 999: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
