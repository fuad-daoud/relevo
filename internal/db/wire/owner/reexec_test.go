//go:build unix

package owner

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// syncBuffer is a Writer the helper's captured output can be read from while it
// runs, without racing the goroutine os/exec writes it with.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The variables a test passes to the test binary when it runs it as its own
// daemon for the re-exec-under-load test.
const (
	helperEnv      = "RELEVO_OWNER_REEXEC_HELPER"
	helperRootEnv  = "RELEVO_OWNER_REEXEC_ROOT"
	helperDBEnv    = "RELEVO_OWNER_REEXEC_DB"
	helperReadyEnv = "RELEVO_OWNER_REEXEC_READY"
	helperAdoptEnv = "RELEVO_OWNER_REEXEC_ADOPTED"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) != "" {
		runReexecHelper()
		return
	}
	os.Exit(m.Run())
}

// setEnv replaces key's value in env instead of appending a second entry: a
// duplicate would make the child's Getenv read the stale one.
func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+val)
}

// helperDSN opens the helper's database with the owner's own pragmas.
func helperDSN(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

// helperListener adopts the inherited descriptor when one is present, else
// binds the socket.
func helperListener() (net.Listener, error) {
	if fd := os.Getenv("RELEVO_LISTEN_FD"); fd != "" {
		n, err := strconv.Atoi(fd)
		if err != nil {
			return nil, err
		}
		ln, err := Adopt(n)
		if err != nil {
			return nil, err
		}
		if adopted := os.Getenv(helperAdoptEnv); adopted != "" {
			_ = os.WriteFile(adopted, []byte("adopted\n"), 0o600)
		}
		return ln, nil
	}
	return Listen(os.Getenv(helperRootEnv))
}

// runReexecHelper is the test binary acting as its own daemon: bind or adopt,
// serve until SIGUSR1, then drain, hand the listener over and exec itself.
func runReexecHelper() {
	ln, err := helperListener()
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper listener:", err)
		os.Exit(1)
	}
	sqlDB, err := sql.Open("sqlite", helperDSN(os.Getenv(helperDBEnv)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper open:", err)
		os.Exit(1)
	}
	srv := New(sqlDB, 3, 9, "01ORIGIN", nil)
	go func() { _ = srv.Serve(ln) }()

	if ready := os.Getenv(helperReadyEnv); ready != "" {
		_ = os.WriteFile(ready, []byte("ready\n"), 0o600)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Drain(ctx)
	cancel()

	f, err := Inherit(ln)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper inherit:", err)
		os.Exit(1)
	}
	env := setEnv(os.Environ(), "RELEVO_LISTEN_FD", strconv.Itoa(int(f.Fd())))
	if err := syscall.Exec(os.Args[0], []string{os.Args[0]}, env); err != nil {
		fmt.Fprintln(os.Stderr, "helper exec:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func waitFile(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func waitForFile(t *testing.T, path, msg string) {
	t.Helper()
	if !waitFile(path, 10*time.Second) {
		t.Fatal(msg)
	}
}

type loadFailure struct {
	at  time.Time
	err error
}

// reexecRoots creates the short root the re-exec test uses and the paths under
// it, and seeds the table the load writes to.
func reexecRoots(t *testing.T) (root, sock, dbPath, ready, adopted string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	sock = filepath.Join(root, socketName)
	dbPath = filepath.Join(root, "o.db")
	ready = filepath.Join(root, "ready")
	adopted = filepath.Join(root, "adopted")

	seed, err := sql.Open("sqlite", helperDSN(dbPath))
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	_ = seed.Close()
	return root, sock, dbPath, ready, adopted
}

// startHelper runs the test binary as its own daemon with the helper variables.
func startHelper(t *testing.T, self, root, dbPath, ready, adopted string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		helperRootEnv+"="+root,
		helperDBEnv+"="+dbPath,
		helperReadyEnv+"="+ready,
		helperAdoptEnv+"="+adopted,
		"RELEVO_LISTEN_FD=",
	)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd, out
}

// runLoadLoop issues requests until stop, recording every failure.
func runLoadLoop(stop <-chan struct{}, db *sql.DB, fails chan<- loadFailure) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		if _, err := db.Exec(`INSERT INTO t (n) VALUES (1)`); err != nil {
			select {
			case fails <- loadFailure{at: time.Now(), err: err}:
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// holdTransaction opens a transaction that stays open until the caller commits,
// so the drain has something to wait for.
func holdTransaction(t *testing.T, sock string) *sql.Conn {
	t.Helper()
	holdDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("hold open: %v", err)
	}
	t.Cleanup(func() { _ = holdDB.Close() })
	holder, err := holdDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold conn: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if _, err := holder.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	return holder
}

// queueRequest sends one request after a short delay, so it lands inside the
// drain window, and returns a channel for its result.
func queueRequest(t *testing.T, sock string) <-chan error {
	t.Helper()
	db, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("queued open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	time.Sleep(100 * time.Millisecond)
	out := make(chan error, 1)
	go func() {
		_, err := db.Exec(`INSERT INTO t (n) VALUES (2)`)
		out <- err
	}()
	return out
}

// assertLoadFailuresWithin requires that every recorded failure happened inside
// the drain window, from the signal to a moment after the new image served.
func assertLoadFailuresWithin(t *testing.T, fails <-chan loadFailure, from, to time.Time) {
	t.Helper()
	for f := range fails {
		if f.at.Before(from) {
			t.Errorf("a request failed before the drain began: %v", f.err)
		}
		if f.at.After(to) {
			t.Errorf("a request failed after the new image was serving: %v", f.err)
		}
	}
}

// awaitAdoption waits for the new image's marker, dumping the helper's goroutine
// stacks when it never arrives.
func awaitAdoption(t *testing.T, cmd *exec.Cmd, adopted string, out *syncBuffer) time.Time {
	t.Helper()
	if waitFile(adopted, 5*time.Second) {
		return time.Now()
	}
	_ = cmd.Process.Signal(syscall.SIGQUIT)
	time.Sleep(500 * time.Millisecond)
	t.Fatalf("the new image never adopted the listener: %s", out.String())
	return time.Time{}
}

// awaitQueued requires that the queued request reached a serving owner.
func awaitQueued(t *testing.T, queued <-chan error) {
	t.Helper()
	select {
	case err := <-queued:
		if err != nil {
			t.Errorf("the queued request was not served by the new image: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued request was never answered")
	}
}

// assertServing checks the load client still works after the exec.
func assertServing(t *testing.T, loadDB *sql.DB) {
	t.Helper()
	if _, err := loadDB.Exec(`INSERT INTO t (n) VALUES (3)`); err != nil {
		t.Errorf("the load client did not recover after the re-exec: %v", err)
	}
}

// TestListenerSurvivesARealReexecUnderLoad runs the test binary as a real
// daemon, drives requests through it, re-execs it with a transaction held open
// and requires: a request queued across the exec is served by the new image,
// the socket file is the same file (no unlink and rebind), and every failure is
// inside the drain window.
func TestListenerSurvivesARealReexecUnderLoad(t *testing.T) {
	root, sock, dbPath, ready, adopted := reexecRoots(t)
	client.SetHandshakeTimeout(10 * time.Second)
	t.Cleanup(func() { client.SetHandshakeTimeout(0) })
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	cmd, out := startHelper(t, self, root, dbPath, ready, adopted)

	waitForFile(t, ready, "the helper never became ready: "+out.String())
	before, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}

	loadDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("load open: %v", err)
	}
	t.Cleanup(func() { _ = loadDB.Close() })

	holder := holdTransaction(t, sock)

	stop := make(chan struct{})
	fails := make(chan loadFailure, 4096)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runLoadLoop(stop, loadDB, fails)
	}()

	signalAt := time.Now()
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("signal helper: %v", err)
	}

	queued := queueRequest(t, sock)
	time.Sleep(300 * time.Millisecond)
	if _, err := holder.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	adoptedAt := awaitAdoption(t, cmd, adopted, out)
	awaitQueued(t, queued)

	after, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket after re-exec: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Errorf("the socket was rebound across the exec: %v is not %v", after, before)
	}

	close(stop)
	wg.Wait()
	close(fails)
	assertLoadFailuresWithin(t, fails, signalAt, adoptedAt.Add(2*time.Second))
	assertServing(t, loadDB)
}
