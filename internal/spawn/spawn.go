// Package spawn owns the process model a headless round runs on: the spec of
// one process, the handle that names a running one, and the Runner that starts,
// observes and stops it.
package spawn

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
)

// ProcSpec is one process a headless builder round runs.
type ProcSpec struct {
	Dir        string     // working directory: the binding's CWD
	Argv       []string   // Argv[0] is the binary name, resolved on PATH by the runner
	Env        []string   // additions to the parent environment; nil for none
	LogPath    string     // stderr, appended, created if absent
	StreamPath string     // stdout and the exit trailer, appended, created if absent
	Scope      *ScopeSpec // non-nil launches the process as a transient systemd scope
}

// ScopeSpec asks the runner to start the process as a transient systemd scope.
// nil on ProcSpec means a plain spawn.
type ScopeSpec struct {
	Unit      string // "relevo-round-<owner8>-<name>-<round>"; the runner appends ".scope"
	Slice     string // "" = omit --slice
	CPUWeight int    // >= 1; always emitted
	MemoryMax string // "" = omit
	CPUQuota  string // "" = omit; systemd units, e.g. "200%" = two cores' worth
	TasksMax  int    // 0 = omit

	// AllowedCPUs is a real launch field: "" omits it, and otherwise it is a
	// systemd cpu-list ("2" or "0-2") pinning the process to those cores.
	AllowedCPUs string

	// GateCPUQuota is the gate's own CPU ceiling, template only. It is set on
	// Runtime.Scope from policy and read only by scopeFor when it builds a
	// gate's spec: scopeFor copies the template, moves this value into
	// CPUQuota, and always returns a spec with GateCPUQuota zeroed. The local
	// Runner (proc.ScopeArgv) never reads it, so a spec handed to Start must
	// have it empty.
	GateCPUQuota string
}

// GoMaxProcsFor reports the GOMAXPROCS a process launched under s should get:
// the number of CPUs that scope may run on. ok false means the scope limits
// nothing, and the caller sets nothing.
//
// It counts AllowedCPUs with policy.ParseCPUList (a pinned round's pool is one
// core) and CPUQuota as ceil(percent/100) with a minimum of 1; with both it is
// the smaller of the two. It reads nothing else: GateCPUQuota is template-only
// and is always zeroed on a launched spec. A malformed AllowedCPUs or CPUQuota
// contributes nothing, as if it were unset -- impossible after policy.Load, but
// the rule must not panic. Pure.
//
// The value is Go-only, and relevo sets it even though Go already derives its
// default GOMAXPROCS from the CPU affinity mask (sched_getaffinity): for a
// pinned round that is redundant but harmless, and it is correct when the pin
// was refused and the round floated. It matters for quota-only rounds, and for
// every Go binary whose main module is below Go 1.25 -- only a 1.25+ main
// module reads the cgroup CPU bandwidth limit (containermaxprocs), so such a
// binary would otherwise size itself for the whole machine. The go command's
// -p and `go test -parallel` defaults follow GOMAXPROCS.
func GoMaxProcsFor(s ScopeSpec) (int, bool) {
	var (
		n    int
		have bool
	)
	if s.AllowedCPUs != "" {
		if cpus, err := policy.ParseCPUList(s.AllowedCPUs); err == nil && len(cpus) > 0 {
			n, have = len(cpus), true
		}
	}
	if s.CPUQuota != "" {
		digits, hasPct := strings.CutSuffix(s.CPUQuota, "%")
		percent, err := strconv.Atoi(digits)
		valid := hasPct && digits != "" && err == nil
		if valid {
			for _, r := range digits {
				if r < '0' || r > '9' {
					valid = false
					break
				}
			}
		}
		if valid {
			q := (percent + 99) / 100
			if q < 1 {
				q = 1
			}
			if have && q > n {
				q = n
			}
			n, have = q, true
		}
	}
	return n, have
}

// RusageTrailerPrefix is the prefix of the rusage line the supervisor appends
// inside a scoped spawn, right before the exit trailer. A payload tail skips
// lines carrying this prefix.
const RusageTrailerPrefix = "relevo-rusage:"

// ExitTrailer prefixes the last line of a builder's stream: the code the
// supervisor left follows it.
const ExitTrailer = "relevo-exit:"

// ProcRusage is what the supervisor measured for the round's cgroup.
type ProcRusage struct {
	CPUMS        int64
	PeakMemBytes int64
}

// ProcHandle names a running process well enough to tell it from a later
// process that reused its pid: the pid and the start time the OS reports.
type ProcHandle struct {
	PID       int
	StartedAt time.Time
}

// Runner starts, observes and stops one process on behalf of a binding. It
// knows nothing about rounds, reports or harnesses. The local implementation is
// internal/proc; a remote one would be ssh.
//
// Start returns as soon as the pid exists; the caller never waits on it.
// Alive is true iff a process with the handle's pid exists AND its start
// time matches within one second -- a reused pid is false, and a missing
// pid is (false, nil), not an error. ExitCode reports the code the runner's
// supervisor left as the stream's last line (the caller passes the stream path),
// ok false when there is none (the process is still running, or was killed
// before it could write one). Kill stops the process group, escalating after a
// grace; not alive is nil.
type Runner interface {
	Start(ctx context.Context, spec ProcSpec) (ProcHandle, error)
	Alive(ctx context.Context, h ProcHandle) (bool, error)
	ExitCode(ctx context.Context, h ProcHandle, logPath string) (code int, ok bool)
	Kill(ctx context.Context, h ProcHandle) error
	// Rusage reports the rusage trailer the supervisor left as the stream's
	// second-to-last line; ok false when absent (plain spawn, killed
	// supervisor, still running).
	Rusage(ctx context.Context, h ProcHandle, streamPath string) (ProcRusage, bool)
}

// ScopeProber is the optional half of a Runner that can say whether a systemd
// scope unit is still occupying its name. Callers type-assert Runtime.Runner to
// it; a Runner that lacks it, or whose probe errors, is treated as "no scope is
// active".
type ScopeProber interface {
	// ScopeActive reports whether the scope unit <unit>.scope is loaded and not
	// yet gone: ActiveState is active, activating, deactivating or reloading.
	// unit is the base name scopeUnitName returns (no ".scope").
	// An error means "could not tell"; callers treat it as not active.
	ScopeActive(ctx context.Context, unit string) (bool, error)
}

// ScopeStopper is the optional half of a Runner that can end a systemd scope
// and everything still in it. Callers type-assert Runtime.Runner to it; a
// Runner that lacks it can see a scope but not end one.
type ScopeStopper interface {
	// StopScope ends the scope unit <unit>.scope and every process still in
	// its cgroup, so a straggler a harness abandoned cannot hold the unit
	// open. It signals first and kills after the runner's own grace, and
	// returns only once the unit is gone; an error means it may still be
	// there. unit is the base name scopeUnitName returns (no ".scope"). A
	// unit that is not loaded, and a host with no systemctl, are not errors:
	// there is nothing to end.
	StopScope(ctx context.Context, unit string) error
}

// ScopeResultProber is the optional half of a Runner that can report the
// final systemd Result of a scope unit. Callers type-assert Runtime.Runner
// to it; a Runner that lacks it, or whose probe errors, is treated as
// "not oom-killed".
type ScopeResultProber interface {
	// ScopeResult is the systemd Result of the scope unit <unit>.scope, e.g.
	// "success" or "oom-kill"; "" when the unit is unknown or systemctl is missing.
	ScopeResult(ctx context.Context, unit string) (string, error)
}

// ErrRunnerUnavailable is returned by a headless path when Runtime.Runner is
// nil: the binary was built or the runtime assembled without one.
var ErrRunnerUnavailable = errors.New("no process runner configured")
