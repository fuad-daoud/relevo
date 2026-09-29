//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/store"
)

// cmdTestHelperEnv marks the one-opener child: TestMain answers it before any
// isolation, so the child keeps the parent's state root.
const cmdTestHelperEnv = "RELEVO_CMDTEST_HELPER"

// shortStateRoot is a state home directly under /tmp: the socket path under
// t.TempDir can exceed the sun_path limit.
func shortStateRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if n := len(filepath.Join(root, "relevo", "relevo.sock")); n >= 104 {
		t.Fatalf("socket path is %d bytes, over the 104-byte sun_path", n)
	}
	return root
}

// machineRoot resolves the state root the current XDG_STATE_HOME selects.
func machineRoot(t *testing.T) string {
	t.Helper()
	root, err := store.DefaultRoot()
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
// root's socket, the way the daemon does.
func startTestOwner(t *testing.T, stateHome string) *testOwner {
	t.Helper()
	root := filepath.Join(stateHome, "relevo")
	d, err := openDBDirect(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := checkSocketPath(root); err != nil {
		t.Fatalf("checkSocketPath: %v", err)
	}
	raw, err := openOwnerListener(root)
	if err != nil {
		t.Fatalf("openOwnerListener: %v", err)
	}
	ln := &countingListener{Listener: raw}
	t.Cleanup(func() { _ = ln.Close() })

	srv, err := serveOwner(d, ln)
	if err != nil {
		t.Fatalf("serveOwner: %v", err)
	}
	if srv == nil {
		t.Fatal("serveOwner returned no owner")
	}
	t.Cleanup(func() { _ = srv.Close() })
	return &testOwner{db: d, ln: ln, srv: srv}
}

// withOwnerRoute installs the production route for the test and clears it when
// the test ends, so the next test stays direct.
func withOwnerRoute(t *testing.T, budget time.Duration) {
	t.Helper()
	installDBRoute(routeOwner, budget)
	t.Cleanup(func() { installDBRoute(routeNone, verbDialBudget) })
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

// TestDirectEnvOpensTheFile pins the hidden escape hatch: with RELEVO_DB_DIRECT
// set the machine path opens the file even when an owner is listening.
func TestDirectEnvOpensTheFile(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	served := startTestOwner(t, root)
	t.Setenv("RELEVO_DB_DIRECT", "1")

	if mode, _ := routeForArgs([]string{"status"}); mode != routeDirect {
		t.Errorf("routeForArgs with RELEVO_DB_DIRECT = %v, want direct", mode)
	}
	installDBRoute(routeDirect, verbDialBudget)
	t.Cleanup(func() { installDBRoute(routeNone, verbDialBudget) })

	d, err := openDB(machineDBPath())
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if got := d.Route(); got != "file" {
		t.Errorf("route = %q, want %q", got, "file")
	}
	if got := atomic.LoadInt32(&served.ln.accepts); got != 0 {
		t.Errorf("the owner accepted %d connections during a direct open, want 0", got)
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

	if mode, _ := routeForArgs([]string{"daemon", "--check"}); mode != routeNone {
		t.Errorf("routeForArgs(daemon --check) = %v, want none", mode)
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
	if got := daemonArgv("/usr/bin/relevo"); !reflect.DeepEqual(got, []string{"/usr/bin/relevo", "daemon"}) {
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

// helperReport is what the one-opener child prints on stdout.
type helperReport struct {
	Version int      `json:"version"`
	DBFDs   []string `json:"db_fds"`
}

// runCmdTestHelper is the child half of TestOneOpenerWithAnOwnerRunning: it
// reaches the machine database through the parent's owner, proves the read,
// and reports every descriptor it holds on the database.
func runCmdTestHelper() int {
	installDBRoute(routeOwner, verbDialBudget)
	d, err := openDB(machineDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: open: %v\n", err)
		return 1
	}
	defer func() { _ = d.Close() }()

	version, err := d.Version()
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: read: %v\n", err)
		return 1
	}
	report, err := json.Marshal(helperReport{Version: version, DBFDs: databaseFDs()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: marshal: %v\n", err)
		return 1
	}
	fmt.Println(string(report))
	return 0
}

// databaseFDs names the open descriptors that point at a relevo database or
// its WAL siblings.
func databaseFDs() []string {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(filepath.Base(target), "relevo.db") {
			out = append(out, target)
		}
	}
	return out
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
