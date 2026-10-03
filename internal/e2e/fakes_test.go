package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

type promptCall struct {
	Target string
	Text   string
}

type scriptRunner struct {
	mu    sync.Mutex
	specs []spawn.ProcSpec
	alive bool
	// gateExit is the scripted exit code per gate run, keyed by the gate log
	// path ExitCode receives: a gate the test has completed with a chosen code,
	// a closed builder round's specs never land here. A dead process whose
	// logPath is not here is today's builder exit, code 0.
	gateExit map[string]int
}

func (r *scriptRunner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	r.alive = true
	return spawn.ProcHandle{PID: 4242, StartedAt: time.Now()}, nil
}

func (r *scriptRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alive, nil
}

func (r *scriptRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alive {
		return 0, false
	}
	if code, ok := r.gateExit[logPath]; ok {
		return code, true
	}
	return 0, true
}

// runningSpec reports the spec of the process the fake runner currently holds
// alive, so a test can tell which served step is in flight. ok is false when
// nothing is running: a finished builder round and a completed gate both leave
// the runner dead.
func (r *scriptRunner) runningSpec() (spawn.ProcSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.alive || len(r.specs) == 0 {
		return spawn.ProcSpec{}, false
	}
	return r.specs[len(r.specs)-1], true
}

// completeGate is the helper the e2e calls to finish a served chain's gate: it
// marks the gate run logging to logPath dead with exit code code, so the next
// server tick reads the scripted code through ExitCode and records pass or
// fail. Keying by logPath is the same seam ExitCode reads.
func (r *scriptRunner) completeGate(t *testing.T, logPath string, code int) {
	t.Helper()
	// A real gate writes its output to the log the runner was started with, and
	// that log is what the server serves back to the client (and what
	// gateSignature and the repair plan read). The fake runner writes one line
	// so the pulled gate log is a real file, then lands the scripted exit.
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("completeGate: create %s: %v", filepath.Dir(logPath), err)
	}
	if err := os.WriteFile(logPath, []byte(fmt.Sprintf("scripted gate output for exit %d\n", code)), 0o644); err != nil {
		t.Fatalf("completeGate: write %s: %v", logPath, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gateExit == nil {
		r.gateExit = map[string]int{}
	}
	r.gateExit[logPath] = code
	r.alive = false
}

// isGateSpec reports whether spec is a gate run rather than a served builder
// round: the gate's own argv is `sh -c <check> 2>&1` (relevo's startGate),
// while a builder round's argv is the harness's own launch line.
func isGateSpec(spec spawn.ProcSpec) bool {
	return len(spec.Argv) >= 2 && spec.Argv[0] == "sh" && spec.Argv[1] == "-c"
}

func (r *scriptRunner) Kill(ctx context.Context, h spawn.ProcHandle, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = false
	return nil
}

func (r *scriptRunner) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	return spawn.ProcRusage{}, false
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}

	// A repo these tests create gets auto-maintenance off. Every `git commit`
	// otherwise spawns `git maintenance run --auto --quiet --detach`, which
	// outlives the command and writes under .git/objects while t.TempDir()'s
	// RemoveAll is removing the tree -- and that cleanup failure fails the
	// test, not just the teardown (#304). Repo-local config, so every later
	// git command on it inherits it, including ones the code under test runs.
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return string(out)
}

func finishRound(t *testing.T, serverRT relevo.Runtime, name string, round int, commitMsg string) {
	t.Helper()
	b, err := serverRT.Store.Load(name)
	if err != nil {
		t.Fatalf("finishRound: read binding %s: %v", name, err)
	}
	worktree := b.Worktree
	if worktree == "" {
		worktree = serverRT.Store.WorktreePath(name)
	}

	reportContent := fmt.Sprintf("# Round %d Report\n\n```relevo\nstatus: done\nchanged_paths: [hello.txt]\n```\n", round)
	if err := os.WriteFile(serverRT.Store.ReportPath(name, round), []byte(reportContent), 0o644); err != nil {
		t.Fatalf("finishRound: write report: %v", err)
	}

	helloPath := filepath.Join(worktree, "hello.txt")
	if err := os.WriteFile(helloPath, []byte("hello from round "+strconv.Itoa(round)+"\n"), 0o644); err != nil {
		t.Fatalf("finishRound: write hello.txt: %v", err)
	}
	runGit(t, worktree, "add", "hello.txt")
	runGit(t, worktree, "commit", "-m", commitMsg)

	// A builder writes its done marker and then exits: in that order, so a
	// server tick running in the background between the two can never see an
	// exited builder with a report but no marker (the unmarked close) -- the
	// race that made TestRemoteRoundCollectedAfterClientWasAway flaky on macOS.
	if err := os.WriteFile(serverRT.Store.DonePath(name, round), []byte(""), 0o644); err != nil {
		t.Fatalf("finishRound: write done marker: %v", err)
	}

	if sr, ok := serverRT.Runner.(*scriptRunner); ok {
		sr.mu.Lock()
		sr.alive = false
		sr.mu.Unlock()
	}
}

// isServedCheckSpec reports whether spec is a served binding's check run rather
// than the gate of a builder round: a check streams to its own served-check log,
// while a round's gate logs to the round's gate log.
func isServedCheckSpec(spec spawn.ProcSpec) bool {
	return strings.HasSuffix(spec.LogPath, "-served-check.log")
}
