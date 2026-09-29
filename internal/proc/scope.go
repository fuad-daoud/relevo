package proc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// ScopeUnitFileName returns the systemd unit file name for a scope unit, built
// here so the supervisor's cgroup guard and systemd-run's --unit= never drift.
func ScopeUnitFileName(unit string) string {
	return unit + ".scope"
}

// ScopeArgv wraps inner (the argv Start would otherwise exec) so it runs as a
// transient systemd --scope unit; systemd-run execs inner in place, so the pid
// Start records is inner's own. The quota precedes the pin, the pin memory.
func ScopeArgv(s spawn.ScopeSpec, inner []string) []string {
	argv := []string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=" + ScopeUnitFileName(s.Unit)}
	if s.Slice != "" {
		argv = append(argv, "--slice="+s.Slice)
	}
	argv = append(argv, "-p", "CPUWeight="+strconv.Itoa(s.CPUWeight))
	if s.CPUQuota != "" {
		argv = append(argv, "-p", "CPUQuota="+s.CPUQuota)
	}
	if s.AllowedCPUs != "" {
		argv = append(argv, "-p", "AllowedCPUs="+s.AllowedCPUs)
	}
	if s.MemoryMax != "" {
		argv = append(argv, "-p", "MemoryMax="+s.MemoryMax)
	}
	if s.TasksMax != 0 {
		argv = append(argv, "-p", "TasksMax="+strconv.Itoa(s.TasksMax))
	}
	argv = append(argv, "--")
	argv = append(argv, inner...)
	return argv
}

// ProbeScopes confirms systemd-run can start a scope under slice before the
// daemon relies on it; a non-nil error means served builders run unscoped.
func ProbeScopes(ctx context.Context, slice string) error {
	unit, err := probeUnit("relevo-probe-")
	if err != nil {
		return fmt.Errorf("systemd-run: %s", err.Error())
	}
	spec := spawn.ScopeSpec{Unit: unit, Slice: slice, CPUWeight: 100}
	return runScopeProbe(ctx, spec, "systemd-run")
}

// ProbeAllowedCPUs confirms systemd-run accepts AllowedCPUs=<cpus> before the
// daemon pins every round; a refused pin leaves the scope and its quota but
// drops the pin. It detects refusal only: a systemd that silently ignores an
// undelegated cpuset reports no error, which `relevo doctor` reports.
func ProbeAllowedCPUs(ctx context.Context, slice, cpus string) error {
	unit, err := probeUnit("relevo-probe-cpus-")
	if err != nil {
		return fmt.Errorf("systemd-run AllowedCPUs=%s: %s", cpus, err.Error())
	}
	spec := spawn.ScopeSpec{Unit: unit, Slice: slice, CPUWeight: 100, AllowedCPUs: cpus}
	return runScopeProbe(ctx, spec, "systemd-run AllowedCPUs="+cpus)
}

// runScopeProbe runs spec's throwaway scope, returning systemd-run's first
// stderr line, or its exit status when it printed nothing, prefixed with prefix.
func runScopeProbe(ctx context.Context, spec spawn.ScopeSpec, prefix string) error {
	argv := ScopeArgv(spec, []string{"true"})
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		line := firstNonEmptyLine(stderr.String())
		if line == "" {
			line = err.Error()
		}
		return fmt.Errorf("%s: %s", prefix, line)
	}
	return nil
}

func probeUnit(prefix string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(suffix), nil
}

// ScopeActive reports whether unit's transient scope is still loaded: its
// ActiveState is active, activating, deactivating or reloading. A missing
// systemctl is (false, nil); any other failure is returned and treated as not
// active by the caller.
func (r *Runner) ScopeActive(ctx context.Context, unit string) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", ScopeUnitFileName(unit))
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "deactivating", "reloading":
		return true, nil
	default:
		return false, nil
	}
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// ScopeResult returns the systemd Result of unit's transient scope, e.g.
// "success" or "oom-kill". A missing systemctl returns ("", nil); any other
// failure is returned and treated as not oom-killed by the caller.
func (r *Runner) ScopeResult(ctx context.Context, unit string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "systemctl", "--user", "show", "--property=Result", "--value", ScopeUnitFileName(unit))
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return firstNonEmptyLine(string(out)), nil
}

// ParseRusageTrailer parses a RusageTrailer line. Missing fields stay zero,
// unknown keys and malformed numbers are ignored, and a line not matching the
// prefix is not ok.
func ParseRusageTrailer(line string) (spawn.ProcRusage, bool) {
	if !strings.HasPrefix(line, spawn.RusageTrailerPrefix) {
		return spawn.ProcRusage{}, false
	}
	var r spawn.ProcRusage
	rest := strings.TrimPrefix(line, spawn.RusageTrailerPrefix)
	for _, field := range strings.Fields(rest) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "cpu_usec":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				r.CPUMS = n / 1000
			}
		case "mem_peak":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				r.PeakMemBytes = n
			}
		}
	}
	return r, true
}
