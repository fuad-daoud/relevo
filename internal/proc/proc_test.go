//go:build unix

package proc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

func TestStartCapturesBothStreamsAndTheExitTrailer(t *testing.T) {
	r := New()
	h, log, stream := start(t, r, "sh", "-c", "echo out; echo err >&2; exit 3")
	if h.PID <= 0 || h.StartedAt.IsZero() {
		t.Fatalf("handle = %+v; want a pid and a start time", h)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got, want := string(data), "out\n\n"+spawn.ExitTrailer+"3\n"; got != want {
		t.Errorf("stream = %q, want stdout, a blank line, then the trailer %q", got, want)
	}
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got, want := string(data), "err\n"; got != want {
		t.Errorf("log = %q, want stderr only %q", got, want)
	}
	code, ok := r.ExitCode(context.Background(), h, stream)
	if !ok || code != 3 {
		t.Errorf("ExitCode(stream) = %d, %v; want 3, true", code, ok)
	}
	if _, ok := r.ExitCode(context.Background(), h, log); ok {
		t.Error("the log carries no trailer any more; ExitCode(log) must be ok=false")
	}
}

func TestTrailerIsOnItsOwnLineAfterAPartialWrite(t *testing.T) {
	r := New()
	h, _, stream := start(t, r, "sh", "-c", "printf 'no newline'; exit 0")
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "no newline\n"+spawn.ExitTrailer+"0\n"; got != want {
		t.Errorf("stream = %q, want %q", got, want)
	}
	if code, ok := r.ExitCode(context.Background(), h, stream); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true after a partial last line", code, ok)
	}
}

func TestStartRunsInDirWithExtraEnv(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "pwd; echo $RELEVO_T3"}, Env: []string{"RELEVO_T3=yes"}, LogPath: log, StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)
	data, _ := os.ReadFile(stream)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("stream = %q; want pwd, env, blank, trailer", data)
	}
	// t.TempDir may sit behind a symlink on macOS; compare resolved paths.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(lines[0])
	if gotDir != wantDir {
		t.Errorf("cwd = %q, want %q", lines[0], dir)
	}
	if lines[1] != "yes" {
		t.Errorf("env line = %q, want yes", lines[1])
	}
	if lines[3] != spawn.ExitTrailer+"0" {
		t.Errorf("trailer = %q", lines[3])
	}
}

func TestStartedProcessInheritsRaisedOOMScore(t *testing.T) {
	if _, err := os.Stat("/proc/self/oom_score_adj"); err != nil {
		t.Skip("no /proc/self/oom_score_adj on this platform")
	}
	r := New()
	h, _, stream := start(t, r, "cat", "/proc/self/oom_score_adj")
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got, want := string(data), "500\n\n"+spawn.ExitTrailer+"0\n"; got != want {
		t.Errorf("stream = %q, want %q (builder must see oom_score_adj 500)", got, want)
	}
}

func TestStartedProcessIsInItsOwnGroupAndKillReturnsWithinGrace(t *testing.T) {
	r := New()
	r.KillGrace = 2 * time.Second
	h, _, stream := start(t, r, "sleep", "60")
	t.Cleanup(func() { _ = syscall.Kill(-h.PID, syscall.SIGKILL) })

	alive, err := r.Alive(context.Background(), h)
	if err != nil || !alive {
		t.Fatalf("Alive right after Start = %v, %v; want true", alive, err)
	}
	pgid, err := syscall.Getpgid(h.PID)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	if pgid != h.PID {
		t.Errorf("supervisor pgid = %d, want its own pid %d (Setsid)", pgid, h.PID)
	}
	if pgid == syscall.Getpgrp() {
		t.Error("supervisor shares the test's process group; it would die with the test")
	}

	began := time.Now()
	if err := r.Kill(context.Background(), h, stream); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if took := time.Since(began); took > r.KillGrace+2*time.Second {
		t.Errorf("Kill took %s; want within the grace plus slack", took)
	}
	alive, err = r.Alive(context.Background(), h)
	if err != nil || alive {
		t.Errorf("Alive after Kill = %v, %v; want false", alive, err)
	}
	if _, ok := r.ExitCode(context.Background(), h, stream); ok {
		t.Error("a killed supervisor writes no trailer; ExitCode must be ok=false")
	}
	// Kill on a dead handle is a no-op.
	if err := r.Kill(context.Background(), h, stream); err != nil {
		t.Errorf("second Kill: %v", err)
	}
}

// The kill record, not the trap, is what makes ok=false hold on a platform
// where a shell defers its trap. Repeat because timing matters.
func TestKilledSupervisorNeverWritesTheTrailer(t *testing.T) {
	for i := 0; i < 20; i++ {
		r := New()
		r.KillGrace = 2 * time.Second
		h, _, stream := start(t, r, "sleep", "60")
		t.Cleanup(func() { _ = syscall.Kill(-h.PID, syscall.SIGKILL) })

		if err := r.Kill(context.Background(), h, stream); err != nil {
			t.Fatalf("iteration %d: Kill: %v", i, err)
		}
		if _, ok := r.ExitCode(context.Background(), h, stream); ok {
			t.Errorf("iteration %d: a killed supervisor writes no trailer; ExitCode must be ok=false", i)
		}
	}
}

func TestAliveIsFalseForAReusedPid(t *testing.T) {
	r := New()
	// Our own pid certainly exists; a start time that is not ours must not match.
	h := spawn.ProcHandle{PID: os.Getpid(), StartedAt: time.Unix(1_000_000, 0)}
	alive, err := r.Alive(context.Background(), h)
	if err != nil || alive {
		t.Errorf("Alive(reused pid) = %v, %v; want false, nil", alive, err)
	}
}

func TestAliveIsFalseNotAnErrorForAMissingPid(t *testing.T) {
	r := New()
	h, _, _ := start(t, r, "sh", "-c", "exit 0")
	waitGone(t, r, h, 5*time.Second)
	// Give the reaper a moment so the pid is gone, not merely a zombie.
	time.Sleep(100 * time.Millisecond)
	alive, err := r.Alive(context.Background(), h)
	if err != nil || alive {
		t.Errorf("Alive(exited) = %v, %v; want false, nil", alive, err)
	}
	alive, err = r.Alive(context.Background(), spawn.ProcHandle{})
	if err != nil || alive {
		t.Errorf("Alive(zero handle) = %v, %v; want false, nil", alive, err)
	}
}

func TestStartRefusesAMissingBinaryBeforeTouchingTheLog(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	assertNeither := func() {
		t.Helper()
		if _, statErr := os.Stat(log); statErr == nil {
			t.Error("a refused Start must not create the log")
		}
		if _, statErr := os.Stat(stream); statErr == nil {
			t.Error("a refused Start must not create the stream")
		}
	}
	_, err := r.Start(context.Background(), spawn.ProcSpec{Dir: dir, Argv: []string{"relevo-no-such-binary-t3"}, LogPath: log, StreamPath: stream})
	if err == nil {
		t.Fatal("Start with a missing binary must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), spawn.ProcSpec{Dir: dir, LogPath: log, StreamPath: stream}); err == nil {
		t.Error("Start with empty Argv must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), spawn.ProcSpec{Dir: filepath.Join(dir, "missing"), Argv: []string{"sh", "-c", "true"}, LogPath: log, StreamPath: stream}); err == nil {
		t.Error("Start with a missing Dir must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), spawn.ProcSpec{Dir: dir, Argv: []string{"sh", "-c", "true"}, LogPath: log}); err == nil {
		t.Error("Start with empty StreamPath must fail")
	}
	assertNeither()
}

// Start must not leave a half-opened spawn behind when the stream cannot open.
func TestStartFailsWhenTheStreamCannotBeOpened(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	_, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "true"},
		LogPath: log, StreamPath: filepath.Join(dir, "missing", "001-builder.jsonl"),
	})
	if err == nil {
		t.Fatal("Start with an unopenable stream path must fail")
	}
	if _, statErr := os.Stat(log); statErr != nil {
		t.Errorf("the log is opened before the stream; want it present: %v", statErr)
	}
}

// A Dir that exists but is not a directory is refused before any file opens.
func TestStartRefusesADirThatIsNotADirectory(t *testing.T) {
	r := New()
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: file, Argv: []string{"sh", "-c", "true"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
	})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("Start with a file as Dir = %v; want a not-a-directory error", err)
	}
}

// A symlink planted at the log path must be refused, and its target never
// created: the state directory is runner-writable.
func TestStartRefusesASymlinkedLog(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	target := filepath.Join(t.TempDir(), "pwned")
	if err := os.Symlink(target, log); err != nil {
		t.Fatal(err)
	}
	_, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "true"}, LogPath: log, StreamPath: stream,
	})
	if err == nil {
		t.Fatal("Start with a symlinked log must fail")
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("Lstat(%q) = %v, want fs.ErrNotExist: a refused Start must not create the link's target", target, statErr)
	}
}

// A symlink planted at the stream path must be refused too, and its target
// never created.
func TestStartRefusesASymlinkedStream(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	target := filepath.Join(t.TempDir(), "pwned")
	if err := os.Symlink(target, stream); err != nil {
		t.Fatal(err)
	}
	_, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "true"}, LogPath: log, StreamPath: stream,
	})
	if err == nil {
		t.Fatal("Start with a symlinked stream must fail")
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("Lstat(%q) = %v, want fs.ErrNotExist: a refused Start must not create the link's target", target, statErr)
	}
}

// A symlink to an existing file must be refused without a byte written through
// it, so the target a runner aimed at is left alone.
func TestStartRefusesASymlinkedStreamToAnExistingFile(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	target := filepath.Join(t.TempDir(), "existing.log")
	const planted = "keep me\n"
	if err := os.WriteFile(target, []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, stream); err != nil {
		t.Fatal(err)
	}
	_, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "true"}, LogPath: log, StreamPath: stream,
	})
	if err == nil {
		t.Fatal("Start with a symlinked stream must fail")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read the link's target: %v", readErr)
	}
	if string(got) != planted {
		t.Errorf("link target = %q, want it byte-unchanged at %q", got, planted)
	}
}

// TestExitCodeReadsOnlyATrailingTrailer covers both trailer spellings.
func TestExitCodeReadsOnlyATrailingTrailer(t *testing.T) {
	r := New()
	dir := t.TempDir()
	cases := map[string]struct {
		body string
		code int
		ok   bool
	}{
		"trailer":            {"noise\n" + spawn.ExitTrailer + "7\n", 7, true},
		"trailer no newline": {spawn.ExitTrailer + "0", 0, true},
		"no trailer":         {"hello\nworld\n", 0, false},
		"trailer not last":   {spawn.ExitTrailer + "1\nmore output\n", 0, false},
		"garbage code":       {spawn.ExitTrailer + "x\n", 0, false},
		"empty":              {"", 0, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".log")
			if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			code, ok := r.ExitCode(context.Background(), spawn.ProcHandle{}, p)
			if code != c.code || ok != c.ok {
				t.Errorf("ExitCode = %d, %v; want %d, %v", code, ok, c.code, c.ok)
			}
		})
	}
	if _, ok := r.ExitCode(context.Background(), spawn.ProcHandle{}, filepath.Join(dir, "absent.log")); ok {
		t.Error("ExitCode on a missing file must be ok=false")
	}
}

// A plain spawn, outside any round's scope, must print no rusage line.
func TestSupervisorEmitsRusageOnlyInScope(t *testing.T) {
	r := New()
	h, _, stream := start(t, r, "true")
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if strings.Contains(string(data), spawn.RusageTrailerPrefix) {
		t.Errorf("stream = %q; a plain spawn outside a relevo-round-*.scope must print no rusage line", data)
	}
	if code, ok := r.ExitCode(context.Background(), h, stream); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}
}

// TestSupervisorEmitsRusageWhenUnitMatches pins the guard's positive half: a
// supervisor told to want its own cgroup's unit does emit the rusage trailer.
func TestSupervisorEmitsRusageWhenUnitMatches(t *testing.T) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup on this platform")
	}
	line := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
	fields := strings.SplitN(line, ":", 3)
	if len(fields) != 3 {
		t.Skipf("unexpected /proc/self/cgroup line %q", line)
	}
	cg := fields[2]
	unit := path.Base(cg)
	if _, err := os.Stat("/sys/fs/cgroup" + cg + "/cpu.stat"); err != nil {
		t.Skip("cpu.stat not readable for this cgroup (CI runner or macOS)")
	}

	// The matching branch also reaps, which here would point the fragment at
	// this test's own cgroup and kill it; ":" keeps the same branch and order.
	script := strings.Replace(supervisorScript, reapBlock, ":", 1)
	if script == supervisorScript {
		t.Fatalf("supervisorScript no longer contains %q; this test would run the real reap against the test's own cgroup", reapBlock)
	}
	out, err := exec.Command("/bin/sh", "-c", script, "relevo-supervisor", unit, "/bin/echo", "hi").Output()
	if err != nil {
		t.Fatalf("run supervisorScript: %v", err)
	}
	stream := string(out)
	if !strings.Contains(stream, "\n"+spawn.RusageTrailerPrefix+"cpu_usec=") {
		t.Errorf("stream = %q; want a %scpu_usec= line", stream, spawn.RusageTrailerPrefix)
	}
	if !strings.HasSuffix(stream, spawn.ExitTrailer+"0\n") {
		t.Errorf("stream = %q; want it to end with %s0", stream, spawn.ExitTrailer)
	}
}

func TestStartWrapsArgvWithScope(t *testing.T) {
	spec := spawn.ProcSpec{
		Argv:  []string{"echo", "hi"},
		Scope: &spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-3", Slice: "relevo.slice", CPUWeight: 100},
	}
	got := buildArgv(spec, "/usr/bin/echo")
	inner := []string{"/bin/sh", "-c", supervisorScript, "relevo-supervisor", ScopeUnitFileName(spec.Scope.Unit), "/usr/bin/echo", "hi"}
	want := ScopeArgv(*spec.Scope, inner)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildArgv = %v, want %v", got, want)
	}
}

// The supervisor's first argument names the scope unit it must be running in.
func TestBuildArgvPassesTheWantedUnit(t *testing.T) {
	t.Run("scoped", func(t *testing.T) {
		spec := spawn.ProcSpec{
			Argv:  []string{"echo", "hi"},
			Scope: &spawn.ScopeSpec{Unit: "relevo-round-abc-x-1", CPUWeight: 100},
		}
		got := buildArgv(spec, "/usr/bin/echo")
		idx := indexOf(got, "relevo-supervisor")
		if idx < 0 || idx+1 >= len(got) {
			t.Fatalf("buildArgv = %v; want a \"relevo-supervisor\" element followed by the wanted unit", got)
		}
		if want := "relevo-round-abc-x-1.scope"; got[idx+1] != want {
			t.Errorf("buildArgv[after relevo-supervisor] = %q, want %q", got[idx+1], want)
		}
		if unitFlag := "--unit=relevo-round-abc-x-1.scope"; indexOf(got, unitFlag) < 0 {
			t.Errorf("buildArgv = %v; want it to contain %q", got, unitFlag)
		}
	})
	t.Run("unscoped", func(t *testing.T) {
		spec := spawn.ProcSpec{Argv: []string{"echo", "hi"}}
		got := buildArgv(spec, "/usr/bin/echo")
		idx := indexOf(got, "relevo-supervisor")
		if idx < 0 || idx+1 >= len(got) {
			t.Fatalf("buildArgv = %v; want a \"relevo-supervisor\" element followed by the wanted unit slot", got)
		}
		if got[idx+1] != "" {
			t.Errorf("buildArgv[after relevo-supervisor] = %q, want \"\" for an unscoped spec", got[idx+1])
		}
		for _, a := range got {
			if strings.Contains(a, "systemd-run") {
				t.Errorf("buildArgv = %v; an unscoped spec must have no systemd-run element", got)
			}
		}
	})
}

func indexOf(argv []string, s string) int {
	for i, a := range argv {
		if a == s {
			return i
		}
	}
	return -1
}

func TestStartStripsDeniedEnv(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "leak")
	t.Setenv("RELEVO_T4", "keep")
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", `echo "k=${TYPESAFE_API_KEY-unset}"; echo "r=$RELEVO_T4"`}, LogPath: log, StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "k=unset") {
		t.Errorf("stream does not contain k=unset: %q", out)
	}
	if !strings.Contains(out, "r=keep") {
		t.Errorf("stream does not contain r=keep: %q", out)
	}
}

// TestStartStripsMasterMindIdentityEnv: a child relevo spawns must never
// inherit a MasterMind or planner identity from the daemon, and a stale
// runner marker from the parent must not shadow the round's own. The child's
// environment carries the round's marker exactly once.
func TestStartStripsMasterMindIdentityEnv(t *testing.T) {
	t.Setenv("RELEVO_MASTERMIND", "mm-parent")
	t.Setenv("RELEVO_PLANNER", "planner-parent")
	t.Setenv("RELEVO_RUNNER", "stale-round")
	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"}, Env: []string{"RELEVO_RUNNER=api"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	var markers []string
	for _, line := range strings.Split(string(data), "\n") {
		switch name, _, _ := strings.Cut(line, "="); name {
		case "RELEVO_MASTERMIND", "RELEVO_PLANNER":
			t.Errorf("child saw %q, want no inherited %s identity", line, name)
		case "RELEVO_RUNNER":
			markers = append(markers, line)
		}
	}
	if want := []string{"RELEVO_RUNNER=api"}; !reflect.DeepEqual(markers, want) {
		t.Errorf("child RELEVO_RUNNER entries = %v, want exactly %v", markers, want)
	}
}

// A refused probe falls back rather than failing the Start: the first row's
// scope is dropped, the second loses only AllowedCPUs.
func TestStartFallsBackWhenProbeRefused(t *testing.T) {
	cases := []struct {
		name       string
		stub       string
		scope      *spawn.ScopeSpec
		wantOutput string
	}{
		{
			name:       "failed scope probe runs the builder unscoped",
			stub:       refusingScopeStub,
			scope:      &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100},
			wantOutput: "ran-unscoped",
		},
		{
			name:       "refused pin keeps the scope but drops AllowedCPUs",
			stub:       refusingPinStub,
			scope:      &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "2"},
			wantOutput: "ran-unpinned-fallback",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubDir := t.TempDir()
			writeStub(t, stubDir, tc.stub)
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			r := New()
			dir := t.TempDir()
			stream := filepath.Join(dir, "001-builder.jsonl")
			h, err := r.Start(context.Background(), spawn.ProcSpec{
				Dir: dir, Argv: []string{"sh", "-c", "echo " + tc.wantOutput},
				LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
				Scope: tc.scope,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitGone(t, r, h, 5*time.Second)

			data, err := os.ReadFile(stream)
			if err != nil {
				t.Fatalf("read stream: %v", err)
			}
			got := string(data)
			if !strings.Contains(got, tc.wantOutput) {
				t.Errorf("stream = %q; want the builder's own output, so the fallback really ran it", got)
			}
			if !strings.Contains(got, "\n"+spawn.ExitTrailer+"0\n") {
				t.Errorf("stream = %q; want the normal %s0 trailer", got, spawn.ExitTrailer)
			}
			if code, ok := r.ExitCode(context.Background(), h, stream); !ok || code != 0 {
				t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
			}
		})
	}
}

// TestStartProbesOnlyWhenAsked counts the stub's invocations: each probe runs
// once per Runner, and a Runner never asked for one never probes.
func TestStartProbesOnlyWhenAsked(t *testing.T) {
	scope := &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100}
	pinned := &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "2"}
	cases := []struct {
		name     string
		stub     func(string) string
		scope    *spawn.ScopeSpec
		wantCall int
	}{
		{"scoped Start probes once", countingScopeStub, scope, 1},
		{"unscoped Start never probes", countingScopeStub, nil, 0},
		{"pinned Start probes the pin once", countingPinStub, pinned, 1},
		{"scope without AllowedCPUs never probes the pin", countingPinStub, scope, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubDir := t.TempDir()
			calls := filepath.Join(t.TempDir(), "calls")
			writeStub(t, stubDir, tc.stub(calls))
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			r := New()
			for i := 0; i < 2; i++ {
				dir := t.TempDir()
				h, err := r.Start(context.Background(), spawn.ProcSpec{
					Dir: dir, Argv: []string{"sh", "-c", "true"},
					LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
					Scope: tc.scope,
				})
				if err != nil {
					t.Fatalf("Start %d: %v", i, err)
				}
				waitGone(t, r, h, 5*time.Second)
			}
			data, err := os.ReadFile(calls)
			if tc.wantCall == 0 {
				if err == nil && strings.TrimSpace(string(data)) != "" {
					t.Errorf("the probe ran (%q); want none", data)
				}
				return
			}
			if err != nil {
				t.Fatalf("read the stub's call log: %v", err)
			}
			if got := len(strings.Fields(string(data))); got != tc.wantCall {
				t.Errorf("the probe ran %d times; want %d", got, tc.wantCall)
			}
		})
	}
}

// A refused pin clears AllowedCPUs on a local copy, not the caller's.
func TestStartDoesNotMutateCallerScope(t *testing.T) {
	stubDir := t.TempDir()
	writeStub(t, stubDir, refusingPinStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	scope := &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "2"}
	before := *scope

	dir := t.TempDir()
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "true"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
		Scope: scope,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	if *scope != before {
		t.Errorf("caller's ScopeSpec mutated: %+v, want %+v", *scope, before)
	}
	if scope.AllowedCPUs != "2" {
		t.Errorf("caller's AllowedCPUs = %q, want it unchanged at 2", scope.AllowedCPUs)
	}
}

// TestStartSetsGoMaxProcs pins the child's GOMAXPROCS in each scope case, and
// that Start never appends to the DeniedEnv package var.
func TestStartSetsGoMaxProcs(t *testing.T) {
	cases := []struct {
		name     string
		parent   string
		stub     string
		scope    *spawn.ScopeSpec
		wantVals []string
	}{
		{
			name: "pinned scope", stub: acceptingStub,
			scope:    &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "1"},
			wantVals: []string{"1"},
		},
		{
			name: "refused pin falls back to the quota", stub: refusingPinStub,
			scope:    &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "1", CPUQuota: "200%"},
			wantVals: []string{"2"},
		},
		{
			name: "dropped scope sets none", stub: refusingScopeStub,
			scope:    &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, CPUQuota: "200%"},
			wantVals: nil,
		},
		{
			name: "scope overrides an inherited GOMAXPROCS", parent: "7", stub: acceptingStub,
			scope:    &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "1"},
			wantVals: []string{"1"},
		},
		{
			name: "scope limiting nothing passes the parent through", parent: "7", stub: acceptingStub,
			scope:    &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100},
			wantVals: []string{"7"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.parent == "" {
				unsetGoMaxProcs(t)
			} else {
				t.Setenv("GOMAXPROCS", tc.parent)
			}
			stubDir := t.TempDir()
			writeStub(t, stubDir, tc.stub)
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			deniedBefore := slices.Clone(DeniedEnv)
			r := New()
			dir := t.TempDir()
			stream := filepath.Join(dir, "001-builder.jsonl")
			h, err := r.Start(context.Background(), spawn.ProcSpec{
				Dir: dir, Argv: []string{"sh", "-c", "env"},
				LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
				Scope: tc.scope,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitGone(t, r, h, 5*time.Second)

			if got := childEnvValues(t, stream, "GOMAXPROCS"); !reflect.DeepEqual(got, tc.wantVals) {
				t.Errorf("child GOMAXPROCS entries = %v, want %v", got, tc.wantVals)
			}
			if !reflect.DeepEqual(DeniedEnv, deniedBefore) {
				t.Errorf("DeniedEnv = %v, want %v (Start must not append to the package var)", DeniedEnv, deniedBefore)
			}
		})
	}
}

// Appending the env entries must not write the caller's Env backing array.
func TestStartDoesNotMutateCallerEnv(t *testing.T) {
	unsetGoMaxProcs(t)
	stubDir := t.TempDir()
	writeStub(t, stubDir, acceptingStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	caller := make([]string, 1, 4)
	caller[0] = "RELEVO_T5=keep"
	before := slices.Clone(caller[:cap(caller)])

	r := New()
	dir := t.TempDir()
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"true"},
		Env:     caller,
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
		Scope: &spawn.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "1"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	if want := []string{"RELEVO_T5=keep"}; !reflect.DeepEqual(caller, want) {
		t.Errorf("caller's Env = %v, want %v", caller, want)
	}
	if full := caller[:cap(caller)]; !reflect.DeepEqual(full, before) {
		t.Errorf("caller's Env backing array = %v, want %v (Start must copy, not append in place)", full, before)
	}
}

// fakeProbeRunner returns a Runner with a counting probe and a movable clock.
func fakeProbeRunner(fail *bool) (*Runner, *int, *time.Time) {
	calls := 0
	now := time.Unix(1_700_000_000, 0)
	r := New()
	r.probe = func(context.Context, string) error {
		calls++
		if *fail {
			return errors.New("systemd-run: failed to start transient scope unit: Permission denied")
		}
		return nil
	}
	r.now = func() time.Time { return now }
	return r, &calls, &now
}

// A failed probe retries only after ScopeReprobeAfter; success is sticky.
func TestScopesUsableRetriesAfterAFailure(t *testing.T) {
	fail := true
	r, calls, now := fakeProbeRunner(&fail)
	ctx := context.Background()
	const slice = "relevo.slice"

	if r.scopesUsable(ctx, slice) {
		t.Fatal("a failed probe must leave scopes unusable")
	}
	if *calls != 1 {
		t.Fatalf("probe calls after the first Start = %d, want 1", *calls)
	}

	// An immediate Start is inside the retry window: no probe, still unscoped.
	if r.scopesUsable(ctx, slice) {
		t.Fatal("still inside the retry window: scopes must stay unusable")
	}
	if *calls != 1 {
		t.Fatalf("probe calls after a Start inside the window = %d, want 1 (must not re-probe)", *calls)
	}

	// Past the window with the probe succeeding, the next Start runs scoped.
	fail = false
	*now = now.Add(ScopeReprobeAfter)
	if !r.scopesUsable(ctx, slice) {
		t.Fatal("the re-probe after ScopeReprobeAfter succeeded; scopes must be usable")
	}
	if *calls != 2 {
		t.Fatalf("probe calls after the re-probe = %d, want 2", *calls)
	}

	// Success is sticky: even far in the future, later Starts never probe.
	*now = now.Add(24 * time.Hour)
	if !r.scopesUsable(ctx, slice) {
		t.Fatal("a successful probe must stay sticky")
	}
	if *calls != 2 {
		t.Fatalf("probe calls after success = %d, want 2 (success is sticky)", *calls)
	}
}

// A failure is taken, and warned about, once, not once per Start.
func TestScopesUsableProbesOncePerFailure(t *testing.T) {
	fail := true
	r, calls, now := fakeProbeRunner(&fail)
	ctx := context.Background()
	const slice = "relevo.slice"

	r.scopesUsable(ctx, slice)
	for i := 0; i < 10; i++ {
		r.scopesUsable(ctx, slice)
	}
	if *calls != 1 {
		t.Fatalf("probe calls after a failure and 10 Starts in the window = %d, want 1 (one warning, not one per Start)", *calls)
	}

	// Past the window a second failure is taken, then silent again.
	*now = now.Add(ScopeReprobeAfter)
	r.scopesUsable(ctx, slice)
	r.scopesUsable(ctx, slice)
	if *calls != 2 {
		t.Fatalf("probe calls after a second failure and one Start = %d, want 2 (one warning per failure)", *calls)
	}
}

// reapCall is the exact text the matching branch uses to reap its scope.
const reapCall = `relevo_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$self"`

// reapBlock is the two-step reap that branch runs: read the supervisor's own
// pid from /proc/self/stat, then call the fragment with it.
const reapBlock = `read -r self _ </proc/self/stat
    [ -n "$self" ] && ` + reapCall

// selfPID is a value no kernel will ever assign as a pid.
const selfPID = 42424242

// runReap runs ReapFragment as production does, against a fake procs file.
func runReap(t *testing.T, procs string, self int) (string, error) {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", ReapFragment+"relevo_reap_scope '"+procs+"' "+strconv.Itoa(self)+"\n").CombinedOutput()
	return string(out), err
}

// Every pid in the file but self gets SIGTERM; absent pids are untouched.
func TestReapScopeTerminatesTheListedProcesses(t *testing.T) {
	a := exec.Command("sleep", "60")
	if err := a.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	t.Cleanup(func() { _ = a.Process.Kill(); _ = a.Wait() })

	b := exec.Command("sleep", "60")
	if err := b.Start(); err != nil {
		t.Fatalf("start B: %v", err)
	}
	t.Cleanup(func() { _ = b.Process.Kill(); _ = b.Wait() })

	procs := filepath.Join(t.TempDir(), "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(a.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write procs file: %v", err)
	}

	out, err := runReap(t, procs, selfPID)
	if err != nil {
		t.Fatalf("reap: %v (output %q)", err, out)
	}
	if out != "" {
		t.Errorf("reap output = %q, want empty: the fragment prints nothing", out)
	}

	// A was listed, so it got SIGTERM; sleep has no handler and dies of it.
	err = a.Wait()
	ws, ok := a.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("A's wait status = %v (%v); want it killed by SIGTERM", a.ProcessState, err)
	}

	// B was not listed: the fragment signals only what its file lists.
	if err := b.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("B is gone (%v); want it untouched: it was not in the procs file", err)
	}
}

// A process that ignores SIGTERM is KILLed, well inside the supervisor's grace.
func TestReapScopeKillsWhatIgnoresTERM(t *testing.T) {
	stubborn := exec.Command("sh", "-c", "trap '' TERM; echo ready; exec sleep 60")
	stdout, err := stubborn.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := stubborn.Start(); err != nil {
		t.Fatalf("start the TERM-ignoring process: %v", err)
	}
	t.Cleanup(func() { _ = stubborn.Process.Kill(); _ = stubborn.Wait() })

	// Wait until sh has run `trap`, or the test would see SIGTERM.
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "ready" {
			err = fmt.Errorf("first line = %q, want %q", line, "ready")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("waiting for the trap: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the TERM-ignoring process did not report ready within 10s")
	}

	procs := filepath.Join(t.TempDir(), "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(stubborn.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write procs file: %v", err)
	}

	start := time.Now()
	out, err := runReap(t, procs, selfPID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("reap: %v (output %q)", err, out)
	}
	if out != "" {
		t.Errorf("reap output = %q, want empty: the fragment prints nothing", out)
	}

	err = stubborn.Wait()
	ws, ok := stubborn.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Errorf("wait status = %v (%v); want SIGKILL, since TERM was ignored", stubborn.ProcessState, err)
	}
	// Well below the 60s sleep, so only the KILL fallback can explain it ending.
	if elapsed > 15*time.Second {
		t.Errorf("reap took %s; want under about 15s (20 polls 0.1s apart)", elapsed)
	}
}

// The reap can never fail the supervisor: a missing procs file is exit 0.
func TestReapScopeIsSilentAndZeroOnAMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-cgroup.procs")
	out, err := runReap(t, missing, selfPID)
	if err != nil {
		t.Errorf("reap of a missing file = %v; want exit 0", err)
	}
	if out != "" {
		t.Errorf("reap output = %q, want empty", out)
	}
}

// The fragment comes first, the reap sits after the rusage line and inside the
// */"$want" branch, and the exit trailer is still the last thing printed.
func TestSupervisorScriptReapsInsideTheWantBranch(t *testing.T) {
	if !strings.HasPrefix(supervisorScript, ReapFragment) {
		t.Error("supervisorScript does not start with ReapFragment: the fragment must be defined before the script calls it")
	}
	const trailer = `printf '\n` + spawn.ExitTrailer + `%s\n' "$rc"`
	if !strings.HasSuffix(supervisorScript, trailer) {
		t.Errorf("supervisorScript does not end with %q: the trailer must stay the stream's last line", trailer)
	}
	if strings.Contains(supervisorScript, "$$") {
		t.Error(`supervisorScript contains $$: systemd-run's unit syntax rewrites it to a single $, so a scoped supervisor would reap itself and lose the exit trailer`)
	}

	caseAt := strings.Index(supervisorScript, `case "$cg" in */"$want")`)
	rusageAt := strings.Index(supervisorScript, `printf '\n`+spawn.RusageTrailerPrefix)
	readAt := strings.Index(supervisorScript, "read -r self _ </proc/self/stat")
	reapAt := strings.Index(supervisorScript, reapCall)
	esacAt := strings.Index(supervisorScript, "esac")
	if caseAt < 0 || rusageAt < 0 || readAt < 0 || reapAt < 0 || esacAt < 0 {
		t.Fatalf("supervisorScript lacks one of the branch, the rusage line, the self-pid read, the reap call or its esac: %q", supervisorScript)
	}
	if caseAt >= rusageAt || rusageAt >= readAt || readAt >= reapAt || reapAt >= esacAt {
		t.Errorf("the read at %d and the reap at %d must sit inside the branch [%d,%d), after the rusage line at %d", readAt, reapAt, caseAt, esacAt, rusageAt)
	}
}

// A spawned env shows exactly one GIT_CONFIG_COUNT and the fsmonitor entry.
func TestStartDisablesFsmonitor(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	if counts := childEnvValues(t, stream, "GIT_CONFIG_COUNT"); !reflect.DeepEqual(counts, []string{"3"}) {
		t.Errorf("child GIT_CONFIG_COUNT entries = %v, want exactly [3]", counts)
	}
	if got, ok := childEnvValue(t, stream, "GIT_CONFIG_KEY_2"); !ok || got != "core.fsmonitor" {
		t.Errorf("child GIT_CONFIG_KEY_2 = %q, %v; want core.fsmonitor, true", got, ok)
	}
	if got, ok := childEnvValue(t, stream, "GIT_CONFIG_VALUE_2"); !ok || got != "false" {
		t.Errorf("child GIT_CONFIG_VALUE_2 = %q, %v; want false, true", got, ok)
	}
}

// TestBuildCmdAppliesCredential pins the identity rule: a spec with a
// credential produces a command whose SysProcAttr carries it with no extra
// groups, and a spec without one stays the serve uid. Setsid must survive both.
func TestBuildCmdAppliesCredential(t *testing.T) {
	spec := spawn.ProcSpec{
		Dir: "/tmp", Argv: []string{"echo", "hi"}, Env: []string{"A=1"},
		Credential: &spawn.Credential{UID: 1234, GID: 5678},
	}
	cmd := buildCmd(spec, "/usr/bin/echo")
	if cmd.SysProcAttr == nil {
		t.Fatal("buildCmd left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.Setsid {
		t.Error("buildCmd must keep Setsid")
	}
	cred := cmd.SysProcAttr.Credential
	if cred == nil {
		t.Fatal("buildCmd did not apply the spec's Credential")
	}
	if cred.Uid != 1234 || cred.Gid != 5678 {
		t.Errorf("credential = %+v, want uid 1234 gid 5678", cred)
	}
	if len(cred.Groups) != 0 {
		t.Errorf("credential Groups = %v, want empty: only the primary gid is set", cred.Groups)
	}

	plain := buildCmd(spawn.ProcSpec{Dir: "/tmp", Argv: []string{"echo", "hi"}}, "/usr/bin/echo")
	if plain.SysProcAttr == nil || plain.SysProcAttr.Credential != nil {
		t.Errorf("buildCmd without a Credential = %+v, want Setsid and a nil Credential", plain.SysProcAttr)
	}
	if plain.SysProcAttr != nil && !plain.SysProcAttr.Setsid {
		t.Error("buildCmd must keep Setsid for a spec with no credential")
	}
	if plain.Dir != "/tmp" {
		t.Errorf("buildCmd Dir = %q, want /tmp", plain.Dir)
	}
}

// TestSpawnEnvDeniesSpecDenyEnv pins that the spec's own DenyEnv names are
// filtered from the inherited parent, while the spec's Env entries survive
// untouched and an unrelated parent entry is kept.
func TestSpawnEnvDeniesSpecDenyEnv(t *testing.T) {
	parent := []string{"XDG_RUNTIME_DIR=/run/1", "KEEP=yes", "CLAUDE_CONFIG_DIR=/root/.claude"}
	got := spawnEnv(parent, []string{"HOME=/home/tenant"}, nil,
		[]string{"XDG_RUNTIME_DIR", "CLAUDE_CONFIG_DIR"})
	if slices.Contains(got, "XDG_RUNTIME_DIR=/run/1") {
		t.Errorf("spawnEnv kept a denied entry: %v", got)
	}
	if slices.Contains(got, "CLAUDE_CONFIG_DIR=/root/.claude") {
		t.Errorf("spawnEnv kept a denied account-home entry: %v", got)
	}
	if !slices.Contains(got, "KEEP=yes") {
		t.Errorf("spawnEnv dropped an unrelated parent entry: %v", got)
	}
	if !slices.Contains(got, "HOME=/home/tenant") {
		t.Errorf("spawnEnv dropped the spec's own HOME entry: %v", got)
	}
}

// The real git reads Start's GIT_CONFIG_* entries and reports false.
func TestStartDisablesFsmonitorForGit(t *testing.T) {

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"git", "-C", repo, "config", "--get", "core.fsmonitor"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	var printed []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, spawn.ExitTrailer) {
			printed = append(printed, line)
		}
	}
	if !reflect.DeepEqual(printed, []string{"false"}) {
		t.Errorf("git config --get core.fsmonitor printed %v; want [false]: relevo disables the fsmonitor through GIT_CONFIG_*", printed)
	}
}
