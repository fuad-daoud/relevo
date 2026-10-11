package proc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// scopeResultTimeout bounds one ScopeResult probe: every attempt and every
// pause between them shares it, so a hung journalctl cannot stall a reconcile.
const scopeResultTimeout = 5 * time.Second

var (
	// journalOutput is the seam a test replaces to script the journal read;
	// runJournal reads the real one.
	journalOutput = runJournal

	// scopeResultRetryPause is the pause between the attempts of one probe: the
	// user manager may log a unit's final result a moment after the daemon sees
	// its process exit, so the first read can be legitimately empty.
	scopeResultRetryPause = 200 * time.Millisecond

	// scopeResultAttempts is how many times one probe reads the journal before
	// giving up. Three attempts 200 ms apart fit in scopeResultTimeout.
	scopeResultAttempts = 3
)

// journalArgv is the one journalctl invocation a scope probe makes: the user
// journal for the unit's scope, rendered as JSON. A non-zero since adds
// --since=@<unix>, so the journal itself drops the older attempts a reused
// unit name carries; the parse also dates each entry. Pure.
func journalArgv(unit string, since time.Time) []string {
	argv := []string{"journalctl", "--user", "USER_UNIT=" + ScopeUnitFileName(unit)}
	if !since.IsZero() {
		argv = append(argv, "--since=@"+strconv.FormatInt(since.Unix(), 10))
	}
	return append(argv, "-o", "json")
}

// runJournal reads the user journal for unit's scope as JSON lines. A missing
// journalctl is (nil, nil): there is no journal to read, which the caller
// treats exactly like an empty journal. Any other failure is returned with
// journalctl's own first stderr line.
func runJournal(ctx context.Context, unit string, since time.Time) ([]byte, error) {
	argv := journalArgv(unit, since)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, nil
		}
		line := firstNonEmptyLine(stderr.String())
		if line == "" {
			line = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", argv[0], line)
	}
	return out, nil
}

// ScopeResult reads unit's scope result from the user journal, retrying
// scopeResultAttempts times scopeResultRetryPause apart inside one
// scopeResultTimeout context. It stops early as soon as an entry carries a
// UNIT_RESULT. A journal that holds no such entry is (zero, nil); only a read
// that never succeeded at all is an error, which callers treat as not
// oom-killed. unit is the base name ScopeUnitFileName takes.
func (r *Runner) ScopeResult(ctx context.Context, unit string, since time.Time) (spawn.ScopeResult, error) {
	cctx, cancel := context.WithTimeout(ctx, scopeResultTimeout)
	defer cancel()

	var (
		lastErr  error
		readable bool
	)
	for attempt := 0; attempt < scopeResultAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-cctx.Done():
				if readable {
					return spawn.ScopeResult{}, nil
				}
				return spawn.ScopeResult{}, lastErr
			case <-time.After(scopeResultRetryPause):
			}
		}
		out, err := journalOutput(cctx, unit, since)
		if err != nil {
			lastErr = err
			continue
		}
		readable = true
		if res := ParseScopeResult(out, since); res.Result != "" {
			return res, nil
		}
	}
	if !readable {
		return spawn.ScopeResult{}, lastErr
	}
	return spawn.ScopeResult{}, nil
}

// scopeJournalEntry is the subset of a journalctl -o json line a scope probe
// reads. __REALTIME_TIMESTAMP is microseconds since the epoch, as a string.
type scopeJournalEntry struct {
	Result    string `json:"UNIT_RESULT"`
	Peak      string `json:"MEMORY_PEAK"`
	Timestamp string `json:"__REALTIME_TIMESTAMP"`
}

// ParseScopeResult reads the UNIT_RESULT and MEMORY_PEAK a unit's journal
// lines carry, ignoring every entry older than since. An entry with no
// timestamp is kept: nothing places it before since, and the journal, not the
// parse, is the witness. A line that is not JSON is skipped, a value that does
// not parse leaves its field at its zero, and a later entry wins. Pure.
func ParseScopeResult(lines []byte, since time.Time) spawn.ScopeResult {
	var res spawn.ScopeResult
	for _, line := range bytes.Split(lines, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e scopeJournalEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if ts, err := strconv.ParseInt(e.Timestamp, 10, 64); err == nil && time.UnixMicro(ts).Before(since) {
			continue
		}
		if e.Result != "" {
			res.Result = e.Result
		}
		if e.Peak != "" {
			if n, err := strconv.ParseInt(e.Peak, 10, 64); err == nil {
				res.PeakBytes = n
			}
		}
	}
	return res
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
