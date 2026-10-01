//go:build unix && !modernc

package db

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReExecReapsAnAbandonedStatement pins the reap end to end: a statement the
// engine will not interrupt abandons its pinned connection, the owner fires its
// hook after the grace, the helper drains and execs, and the new image serves a
// query. It is excluded on modernc, whose driver interrupts the statement so
// nothing is left to reap.
func TestReExecReapsAnAbandonedStatement(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	path := filepath.Join(root, "relevo.db")
	ready := filepath.Join(root, "ready")
	adopted := filepath.Join(root, "adopted")
	opened := filepath.Join(root, "opened")
	reap := filepath.Join(root, "reap")
	sock := filepath.Join(root, "relevo.sock")

	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	env := envWithout(os.Environ(), "RELEVO_DBTEST_OWNER")
	env = replaceEnv(env, reexecHelperEnv, "1")
	env = replaceEnv(env, reexecRootEnv, root)
	env = replaceEnv(env, reexecPathEnv, path)
	env = replaceEnv(env, reexecReadyEnv, ready)
	env = replaceEnv(env, reexecAdoptedEnv, adopted)
	env = replaceEnv(env, reexecOpenedEnv, opened)
	env = replaceEnv(env, reexecReapEnv, reap)
	env = replaceEnv(env, reexecGraceEnv, "300ms")
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

	// A statement the engine will not interrupt: the client's deadline is what
	// returns, and the pinned owner connection is left stepping.
	d, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial the helper: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	const runaway = `WITH c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c`
	if _, qerr := d.sqlDB.QueryContext(ctx, runaway); !errors.Is(qerr, context.DeadlineExceeded) {
		cancel()
		t.Fatalf("the runaway query = %v, want the deadline", qerr)
	}
	cancel()
	_ = d.Close()

	waitForPath(t, reap, "the owner never reaped the abandoned statement")
	waitForPath(t, adopted, "the new image never adopted the listener")
	waitForPath(t, opened, "the new image never opened the database")

	d2, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial the new image: %v", err)
	}
	defer func() { _ = d2.Close() }()
	var one int
	if err := d2.sqlDB.QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("query through the new image: %v", err)
	}
	if one != 1 {
		t.Errorf("SELECT 1 = %d, want 1", one)
	}

	// One registration, one hook call: a reap loop would keep appending lines.
	time.Sleep(500 * time.Millisecond)
	raw, err := os.ReadFile(reap)
	if err != nil {
		t.Fatalf("read the reap marker: %v", err)
	}
	if got := strings.Count(string(raw), "\n"); got != 1 {
		t.Errorf("the reap marker has %d lines, want exactly 1", got)
	}
}
