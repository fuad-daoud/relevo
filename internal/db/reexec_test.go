//go:build unix

package db

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// The re-exec helper protocol: the test binary runs itself as its own image,
// serves the database, and on SIGUSR1 hands the listener to the next image.
const (
	reexecHelperEnv  = "RELEVO_DB_REEXEC_HELPER"
	reexecRootEnv    = "RELEVO_DB_REEXEC_ROOT"
	reexecPathEnv    = "RELEVO_DB_REEXEC_PATH"
	reexecReadyEnv   = "RELEVO_DB_REEXEC_READY"
	reexecAdoptedEnv = "RELEVO_DB_REEXEC_ADOPTED"
	reexecOpenedEnv  = "RELEVO_DB_REEXEC_OPENED"
)

// replaceEnv sets key=value in env, replacing any inherited duplicate so the
// child's Getenv cannot read a stale value.
func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return append(out, prefix+value)
}

// waitForPath waits for path to appear, failing the test when it does not.
func waitForPath(t *testing.T, path, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// writeMarker writes a one-line file, ignoring an empty path.
func writeMarker(path string) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte("1\n"), 0o600)
}

// TestDBReexecHelper is the helper the re-exec test runs as its own image. It
// does nothing unless RELEVO_DB_REEXEC_HELPER is set.
func TestDBReexecHelper(t *testing.T) {
	if os.Getenv(reexecHelperEnv) == "" {
		t.Skip("not the re-exec helper")
	}
	runDBReexecHelper(t)
}

// runDBReexecHelper opens and serves the database, then on SIGUSR1 drains the
// owner and execs itself with RELEVO_LISTEN_FD, exactly as the daemon does. The
// next image adopts the listener and opens the database with a zero open-lock
// wait: it succeeds only if no file descriptor of the previous image's open
// lock survived the exec.
func runDBReexecHelper(t *testing.T) {
	root := os.Getenv(reexecRootEnv)
	path := os.Getenv(reexecPathEnv)
	inherited := os.Getenv("RELEVO_LISTEN_FD")

	var (
		ln  net.Listener
		err error
	)
	if inherited != "" {
		fd, aerr := strconv.Atoi(inherited)
		if aerr != nil {
			t.Fatalf("listen fd: %v", aerr)
		}
		if ln, err = owner.Adopt(fd); err != nil {
			t.Fatalf("adopt: %v", err)
		}
		// The new image must not wait: everything the previous image held must
		// have closed with the exec.
		openLockWait = 0
	} else {
		if ln, err = owner.Listen(root); err != nil {
			t.Fatalf("listen: %v", err)
		}
	}

	d, err := OpenWith(path, Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "reexec helper open:", err)
		os.Exit(1)
	}
	srv := NewOwner(d)
	go func() { _ = srv.Serve(ln) }()

	if inherited != "" {
		writeMarker(os.Getenv(reexecAdoptedEnv))
	}
	writeMarker(os.Getenv(reexecOpenedEnv))
	if inherited == "" {
		writeMarker(os.Getenv(reexecReadyEnv))
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Drain(ctx)
	cancel()

	f, err := owner.Inherit(ln)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reexec helper inherit:", err)
		os.Exit(1)
	}
	env := replaceEnv(os.Environ(), "RELEVO_LISTEN_FD", strconv.Itoa(int(f.Fd())))
	if err := syscall.Exec(os.Args[0], os.Args, env); err != nil {
		fmt.Fprintln(os.Stderr, "reexec helper exec:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestReexecNewImageOpensTheDatabaseAtOnce pins the product requirement: the
// image that execs closes its open lock's descriptor with the exec, so the next
// image opens the database at once instead of blocking on a lock the previous
// image left behind.
func TestReexecNewImageOpensTheDatabaseAtOnce(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	path := filepath.Join(root, "relevo.db")
	ready := filepath.Join(root, "ready")
	adopted := filepath.Join(root, "adopted")
	opened := filepath.Join(root, "opened")

	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	// dbtest owner mode is cleared: this helper opens and holds the file itself.
	env := envWithout(os.Environ(), "RELEVO_DBTEST_OWNER")
	env = replaceEnv(env, reexecHelperEnv, "1")
	env = replaceEnv(env, reexecRootEnv, root)
	env = replaceEnv(env, reexecPathEnv, path)
	env = replaceEnv(env, reexecReadyEnv, ready)
	env = replaceEnv(env, reexecAdoptedEnv, adopted)
	env = replaceEnv(env, reexecOpenedEnv, opened)
	env = replaceEnv(env, "RELEVO_LISTEN_FD", "")

	cmd := exec.Command(self, "-test.run=TestDBReexecHelper")
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	waitForPath(t, ready, "the helper never became ready")
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("signal the helper: %v", err)
	}
	waitForPath(t, adopted, "the new image never adopted the listener")
	waitForPath(t, opened, "the new image never opened the database: the previous image's lock survived the exec")

	d, err := Dial(filepath.Join(root, "relevo.sock"))
	if err != nil {
		t.Fatalf("dial the new image: %v", err)
	}
	defer func() { _ = d.Close() }()
	var one int
	if err := d.sqlDB.QueryRowContext(context.Background(), `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("client query through the new image: %v", err)
	}
	if one != 1 {
		t.Errorf("SELECT 1 = %d, want 1", one)
	}
}
