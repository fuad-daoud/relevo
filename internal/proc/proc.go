//go:build unix

// Package proc is relevo's local process Runner: it starts a builder detached
// from relevo, reports whether that exact process is still running, reads the
// exit code its supervisor left in the stream, and stops it.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// DefaultKillGrace is how long Kill waits after SIGTERM before SIGKILL.
const DefaultKillGrace = 5 * time.Second

// ReapFragment is the supervisor's scope reap: a POSIX sh function
// relevo_reap_scope <procs_file> <self_pid> that TERMs, then KILLs, every other
// pid in the scope's cgroup, so a straggler the harness abandoned cannot keep
// the scope alive. The file is read with the builtin read, never cat -- a cat
// subprocess would join the cgroup being reaped -- and every error is
// discarded, so an unemptiable scope never costs the builder its exit trailer.
const ReapFragment = `relevo_reap_scope() {
  procs=$1
  self=$2
  while read -r pid; do
    [ "$pid" = "$self" ] || kill -TERM "$pid" 2>/dev/null
  done 2>/dev/null <"$procs"
  i=0
  while [ "$i" -lt 20 ]; do
    left=0
    while read -r pid; do
      [ "$pid" = "$self" ] || left=1
    done 2>/dev/null <"$procs"
    [ "$left" = 0 ] && break
    sleep 0.1
    i=$((i + 1))
  done
  while read -r pid; do
    [ "$pid" = "$self" ] || kill -KILL "$pid" 2>/dev/null
  done 2>/dev/null <"$procs"
  return 0
}
`

// supervisorScript runs the builder with stdin closed and appends the trailer
// whatever happens to it; plain sh, no bash-isms. Its first argument is the
// scope unit Start expects this process to run in, or "" for a plain spawn.
// It raises oom_score_adj so the kernel prefers a builder over the daemon under
// memory pressure, and keeps the builder a child (no exec) so an OOM kill still
// leaves a trailer. Only when its own cgroup matches that unit does it print a
// rusage line and reap the scope: a plain spawn from inside a round's scope
// inherits that cgroup too. The reap's self pid comes from /proc/self/stat,
// never $$, which systemd-run's unit syntax rewrites to a single $ and which
// would make the supervisor reap itself; a TERM exits 143 before the trailer.
const supervisorScript = ReapFragment + `want=$1
shift
trap 'exit 143' TERM
{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true
"$@" </dev/null; rc=$?
if [ -n "$want" ]; then
  cg=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
  case "$cg" in */"$want")
    u=$(awk '/^usage_usec/{print $2}' "/sys/fs/cgroup$cg/cpu.stat" 2>/dev/null)
    m=$(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null)
    printf '\nrelevo-rusage:%s%s\n' "${u:+cpu_usec=$u}" "${m:+ mem_peak=$m}"
    read -r self _ </proc/self/stat
    [ -n "$self" ] && relevo_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$self"
    ;;
  esac
fi
printf '\nrelevo-exit:%s\n' "$rc"`

type probeState int

const (
	probeUnknown probeState = iota // never probed
	probeOK                        // scopes work; sticky
	probeFailed                    // the last probe failed
)

// ScopeReprobeAfter is how long a failed scope probe is trusted before a scoped
// Start retries it, so one transient failure costs at most this long.
const ScopeReprobeAfter = 5 * time.Minute

// Runner is the local spawn.Runner.
type Runner struct {
	// KillGrace is the SIGTERM-to-SIGKILL grace; zero means DefaultKillGrace.
	KillGrace time.Duration

	// probeMu guards the probe state below: one probe on the first scoped
	// Start, retried after a failure at least ScopeReprobeAfter later.
	probeMu sync.Mutex
	// scopes is the probe's verdict, and scopesFailedAt when it failed.
	scopes         probeState
	scopesFailedAt time.Time
	// probe runs the scope probe; nil means ProbeScopes, tests inject one.
	probe func(ctx context.Context, slice string) error
	// now is the probe's clock; nil means time.Now, tests step it forward.
	now func() time.Time

	// pinOnce guards the lazy pin probe: the first Start whose scope carries an
	// AllowedCPUs pool probes systemd-run with it, and later Starts reuse that.
	pinOnce sync.Once
	// pinOK is that verdict: false means later Starts drop the pin only.
	pinOK bool
}

var _ spawn.Runner = (*Runner)(nil)

var _ spawn.ScopeProber = (*Runner)(nil)

func New() *Runner { return &Runner{} }

func (r *Runner) grace() time.Duration {
	if r.KillGrace > 0 {
		return r.KillGrace
	}
	return DefaultKillGrace
}

// scopesUsable reports whether a scoped spawn may run in its own scope right
// now: the probe is taken when the verdict is unknown or a failure is at least
// ScopeReprobeAfter old, and a success is sticky. A failure is logged once,
// when it is taken.
func (r *Runner) scopesUsable(ctx context.Context, slice string) bool {
	r.probeMu.Lock()
	defer r.probeMu.Unlock()

	now := r.nowTime()
	retry := r.scopes == probeFailed && now.Sub(r.scopesFailedAt) >= ScopeReprobeAfter
	if r.scopes == probeUnknown || retry {
		probe := r.probe
		if probe == nil {
			probe = ProbeScopes
		}
		if err := probe(ctx, slice); err != nil {
			r.scopes = probeFailed
			r.scopesFailedAt = now
			slog.Warn("scopes unavailable; builders will run in this process's cgroup", "err", err)
		} else {
			if retry {
				slog.Info("scopes available again; builders will run in their own scopes")
			}
			r.scopes = probeOK
		}
	}
	return r.scopes == probeOK
}

// nowTime is the probe clock: the injected one in tests, else time.Now.
func (r *Runner) nowTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// buildArgv builds the argv Start execs: bin under supervisorScript, wrapped in
// a systemd scope when spec.Scope is set. The supervisor's first argument is the
// scope unit Start expects to be running in, or "" for a plain spawn.
func buildArgv(spec spawn.ProcSpec, bin string) []string {
	var want string
	if spec.Scope != nil {
		want = ScopeUnitFileName(spec.Scope.Unit)
	}
	inner := append([]string{"/bin/sh", "-c", supervisorScript, "relevo-supervisor", want, bin}, spec.Argv[1:]...)
	if spec.Scope != nil {
		return ScopeArgv(*spec.Scope, inner)
	}
	return inner
}

// buildCmd assembles the exec.Cmd for a spec resolved against bin: the argv,
// the working directory, the filtered child environment and the process
// attributes. It is pure -- no file is opened and no process started -- so the
// credential and environment rules can be pinned without spawning. Start sets
// the stdout and stderr file handles the caller opened; everything else about
// the command is decided here.
func buildCmd(spec spawn.ProcSpec, bin string) *exec.Cmd {
	argv := buildArgv(spec, bin)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spawnEnv(os.Environ(), spec.Env, spec.Scope, spec.DenyEnv)
	cmd.Stdin = nil
	cmd.SysProcAttr = sysProcAttr(spec)
	return cmd
}

// sysProcAttr is the process attribute block a spec starts with: a new session
// always, so Kill's process group is the child's own, and the tenant credential
// only when one was asked for. A nil credential leaves the process running as
// the serve uid -- the none-mode default.
func sysProcAttr(spec spawn.ProcSpec) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setsid: true}
	if spec.Credential != nil {
		attr.Credential = &syscall.Credential{Uid: spec.Credential.UID, Gid: spec.Credential.GID}
	}
	return attr
}

// Start launches spec under a detached supervisor and returns its handle
// without waiting. exec.Command, not CommandContext: the caller's context
// ending must not kill a builder relevo meant to leave running. Setsid keeps the
// supervisor out of relevo's session and out of any group Kill could hit.
func (r *Runner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	bin, err := checkSpawnSpec(spec)
	if err != nil {
		return spawn.ProcHandle{}, err
	}
	logf, streamf, err := openSpawnFiles(spec)
	if err != nil {
		return spawn.ProcHandle{}, err
	}
	defer func() { _ = logf.Close() }()
	defer func() { _ = streamf.Close() }()

	spec = r.resolveScope(ctx, spec)
	cmd := buildCmd(spec, bin)
	cmd.Stdout = streamf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		return spawn.ProcHandle{}, fmt.Errorf("proc: start: %w", err)
	}
	pid := cmd.Process.Pid
	// Reap the supervisor here, if this process outlives it (the daemon does);
	// a short-lived CLI exits first and init reaps instead.
	go func() { _ = cmd.Wait() }()

	return handleFor(ctx, pid), nil
}

// checkSpawnSpec validates spec and resolves the binary Start will exec; every
// check runs before either file is created, so a refused Start leaves nothing.
func checkSpawnSpec(spec spawn.ProcSpec) (string, error) {
	if len(spec.Argv) == 0 {
		return "", errors.New("proc: empty argv")
	}
	if spec.StreamPath == "" {
		return "", errors.New("proc: empty stream path")
	}
	info, err := os.Stat(spec.Dir)
	if err != nil {
		return "", fmt.Errorf("proc: dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("proc: %s is not a directory", spec.Dir)
	}
	bin, err := exec.LookPath(spec.Argv[0])
	if err != nil {
		return "", fmt.Errorf("proc: %w", err)
	}
	return bin, nil
}

// openSpawnFiles opens the builder's log and stream for append.
func openSpawnFiles(spec spawn.ProcSpec) (logf, streamf *os.File, err error) {
	logf, err = openAppend(spec.LogPath)
	if err != nil {
		return nil, nil, fmt.Errorf("proc: log: %w", err)
	}
	streamf, err = openAppend(spec.StreamPath)
	if err != nil {
		_ = logf.Close()
		return nil, nil, fmt.Errorf("proc: stream: %w", err)
	}
	return logf, streamf, nil
}

// oNoFollow is O_NOFOLLOW where the platform has it: the state directory that
// holds a round's log and stream is runner-writable, and the flag is the race
// backstop behind the Lstat refusal, so a link swapped in after the check
// cannot be followed.
const oNoFollow = syscall.O_NOFOLLOW

// openRegular opens path with the given flags, refusing anything that is not a
// regular file. The state directory that holds a round's log, stream and kill
// record is runner-writable, so a symlink planted at any of them must not be
// followed out of it: the Lstat is the refusal and O_NOFOLLOW the race backstop
// behind it. The Lstat also keeps a planted fifo from blocking the open, which
// would wait for a reader that never comes.
func openRegular(path string, flags int) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, flags|os.O_CREATE|os.O_WRONLY|oNoFollow, 0o644)
}

func openAppend(path string) (*os.File, error) {
	return openRegular(path, os.O_APPEND)
}

// resolveScope applies both lazy probes and their fallbacks: a scope whose probe
// fails is dropped, so the builder runs unscoped, and a refused AllowedCPUs is
// cleared while the scope and its quota stay. The pin fallback re-points a
// copied ScopeSpec, since Start took spec by value but Scope is a pointer.
func (r *Runner) resolveScope(ctx context.Context, spec spawn.ProcSpec) spawn.ProcSpec {
	if spec.Scope == nil {
		return spec
	}
	if !r.scopesUsable(ctx, spec.Scope.Slice) {
		spec.Scope = nil
		return spec
	}
	if spec.Scope.AllowedCPUs == "" {
		return spec
	}
	r.pinOnce.Do(func() {
		if err := ProbeAllowedCPUs(ctx, spec.Scope.Slice, spec.Scope.AllowedCPUs); err != nil {
			slog.Warn("cpu pinning unavailable; scopes will run without AllowedCPUs", "allowed_cpus", spec.Scope.AllowedCPUs, "err", err)
			r.pinOK = false
			return
		}
		r.pinOK = true
	})
	if !r.pinOK {
		sc := *spec.Scope
		sc.AllowedCPUs = ""
		spec.Scope = &sc
	}
	return spec
}

// spawnEnv adds the GOMAXPROCS and fsmonitor entries and filters the parent so
// the child sees exactly one of each; the full-slice expressions copy, so the
// caller's Env array, the DeniedEnv var and the spec's DenyEnv are never
// written in place. specDeny names extra variables the spec itself refuses.
func spawnEnv(parent, extra []string, scope *spawn.ScopeSpec, specDeny []string) []string {
	add := goMaxProcsEnv(parent, extra, scope)
	env := append(extra[:len(extra):len(extra)], add...)
	env = append(env[:len(env):len(env)], gitNoFsmonitorEnv(parent, env)...)

	deny := append(DeniedEnv[:len(DeniedEnv):len(DeniedEnv)], specDeny...)
	deny = append(deny, "GIT_CONFIG_COUNT")
	if len(add) > 0 {
		deny = append(deny[:len(deny):len(deny)], "GOMAXPROCS")
	}
	return ChildEnv(parent, deny, env)
}

// handleFor returns the handle for a just-started pid; when ps cannot report a
// start time, time.Now is within the tolerance Alive allows.
func handleFor(ctx context.Context, pid int) spawn.ProcHandle {
	started, _, err := psInfo(ctx, pid)
	if err != nil {
		started = time.Now()
	}
	return spawn.ProcHandle{PID: pid, StartedAt: started.Truncate(time.Second)}
}

// Alive reports whether the handle's process exists, is not a zombie, and
// started within a second of when the handle says. A missing pid is (false,
// nil); only ps itself failing to run is an error.
func (r *Runner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	if h.PID <= 0 {
		return false, nil
	}
	started, state, err := psInfo(ctx, h.PID)
	if errors.Is(err, errNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(state, "Z") {
		return false, nil
	}
	diff := started.Sub(h.StartedAt)
	if diff < 0 {
		diff = -diff
	}
	return diff <= time.Second, nil
}

// ExitCode reads the trailer the supervisor appended, if it is the stream's last
// line. A kill recorded for this handle returns ok=false regardless of what the
// stream ends with.
func (r *Runner) ExitCode(_ context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	if killRecorded(h, logPath) {
		return 0, false
	}
	line, ok := lastLine(logPath)
	if !ok {
		return 0, false
	}
	if !strings.HasPrefix(line, spawn.ExitTrailer) {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimPrefix(line, spawn.ExitTrailer))
	if err != nil {
		return 0, false
	}
	return code, true
}

// Kill sends SIGTERM to the supervisor's process group -- the supervisor and the
// builder under it -- waits up to the grace for Alive to turn false, then
// SIGKILLs the group. Alive's start-time check runs first, so a reused pid is
// never signalled. The record precedes the signal, because a reader only ever
// reads a dead handle, so the record is in place before the process could die.
// A record that cannot be written is warned about, never a reason to leave a
// process alive.
func (r *Runner) Kill(ctx context.Context, h spawn.ProcHandle, streamPath string) error {
	alive, err := r.Alive(ctx, h)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	if err := recordKill(h, streamPath); err != nil {
		slog.Warn("kill record not written; signalling anyway", "stream", streamPath, "err", err)
	}
	if err := syscall.Kill(-h.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("proc: SIGTERM %d: %w", h.PID, err)
	}
	deadline := time.Now().Add(r.grace())
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		alive, err := r.Alive(ctx, h)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
	}
	if err := syscall.Kill(-h.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("proc: SIGKILL %d: %w", h.PID, err)
	}
	return nil
}

// Rusage scans the last few lines of streamPath, from last to first, for the
// rusage trailer; ok is false when none match. The scan is needed because the
// supervisor's printf leaves a blank line between the rusage and exit trailers.
func (r *Runner) Rusage(_ context.Context, _ spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	lines, ok := lastLines(streamPath, 6)
	if !ok {
		return spawn.ProcRusage{}, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], spawn.RusageTrailerPrefix) {
			return ParseRusageTrailer(lines[i])
		}
	}
	return spawn.ProcRusage{}, false
}

var errNoProcess = errors.New("proc: no such process")

// StartTime reports when a process started, via the same `ps -o lstart=` read
// psInfo uses. `relevo mastermind init` needs it to defend a mastermind record against
// pid reuse, as a binding's endpoint does. A missing pid is an error, not the
// zero time: the caller decides what a host it cannot measure means.
func StartTime(ctx context.Context, pid int) (time.Time, error) {
	started, _, err := psInfo(ctx, pid)
	if err != nil {
		return time.Time{}, err
	}
	return started, nil
}

// psLayout is what `ps -o lstart=` prints on Linux (procps) and macOS, where
// the day may be space-padded -- _2 accepts both.
const psLayout = "Mon Jan _2 15:04:05 2006"

// psInfo asks ps for one process's start time and state: ps is the one portable
// source of a start time, since /proc is Linux-only and sysctl needs cgo. A
// failed ps is classified; a signalled or cancelled one is a plain error, so
// every Alive caller treats the process as alive this tick rather than dead.
func psInfo(ctx context.Context, pid int) (started time.Time, state string, err error) {
	out, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		var exit *exec.ExitError
		errors.As(err, &exit) // nil when ps never ran (e.g. not on PATH)
		switch classifyPS(ctx.Err(), exit, out) {
		case psNoProcess:
			return time.Time{}, "", errNoProcess
		default:
			return time.Time{}, "", fmt.Errorf("proc: ps: %w", err)
		}
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return time.Time{}, "", errNoProcess
	}
	if len(fields) < 6 {
		return time.Time{}, "", fmt.Errorf("proc: unexpected ps output %q", strings.TrimSpace(string(out)))
	}
	started, err = time.ParseInLocation(psLayout, strings.Join(fields[1:6], " "), time.Local)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("proc: parse ps start time %q: %w", strings.Join(fields[1:6], " "), err)
	}
	return started, fields[0], nil
}

// lastLine returns the file's final line, ignoring trailing newlines.
func lastLine(path string) (string, bool) {
	lines, ok := lastLines(path, 1)
	if !ok {
		return "", false
	}
	return lines[len(lines)-1], true
}

// lastLines returns up to the final n non-empty lines of the file, oldest first,
// reading only its tail; ok is false for a missing or empty file.
func lastLines(path string, n int) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return nil, false
	}
	const tail = 4096
	off := info.Size() - tail
	if off < 0 {
		off = 0
	}
	buf, err := io.ReadAll(io.NewSectionReader(f, off, info.Size()-off))
	if err != nil {
		return nil, false
	}
	s := strings.TrimRight(string(buf), "\n")
	if s == "" {
		return nil, false
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, true
}
