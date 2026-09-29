package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// RunningProc is one local process a daemon restart could kill: the binding
// it belongs to, which of the round's roles it plays, and its pid.
type RunningProc struct {
	Binding string
	Kind    string // "builder", "gate" or "consult"
	PID     int
}

// ParseUnifiedCgroup returns the cgroup path of the `0::<path>` line of
// /proc/<pid>/cgroup. A file with no such line (a cgroup v1 host, or an
// unknown format) reports ok false, which RestartSafety reads as "cannot
// tell".
func ParseUnifiedCgroup(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, true
		}
	}
	return "", false
}

// inOwnScope reports whether a cgroup path's last element is a relevo scope:
// the process is in its own `relevo-*.scope` and survives a daemon restart.
func inOwnScope(cgroupPath string) bool {
	base := path.Base(cgroupPath)
	return strings.HasPrefix(base, "relevo-") && strings.HasSuffix(base, ".scope")
}

// RestartSafety is the doctor row that says whether a daemon restart right
// now would kill anything. It never fails: a not-exist cgroup read means the
// process exited and is skipped, and any other unreadable host (macOS, a
// cgroup v1 host) degrades to `cannot tell on this host`. The Warn detail
// names at most three offenders, then `and K more`.
func RestartSafety(procs []RunningProc, readCgroup func(pid int) (string, error)) Check {
	c := Check{Group: "", Name: "restart"}
	if len(procs) == 0 {
		return restartOK(c, "no rounds running")
	}

	running := 0
	var offenders []string
	for _, p := range procs {
		content, err := readCgroup(p.PID)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return restartCannotTell(c)
		}
		cgPath, ok := ParseUnifiedCgroup(content)
		if !ok {
			return restartCannotTell(c)
		}
		running++
		if !inOwnScope(cgPath) {
			offenders = append(offenders, fmt.Sprintf("%s %s pid %d in %s", p.Binding, p.Kind, p.PID, path.Base(cgPath)))
		}
	}

	// Every process read as not-exist: on Linux they simply exited, but on a
	// host with no /proc that is every process, and the two cannot be told
	// apart.
	if running == 0 {
		return restartCannotTell(c)
	}
	if len(offenders) == 0 {
		return restartOK(c, fmt.Sprintf("%d running, each in its own scope; a daemon restart leaves them running", running))
	}
	c.Severity = SevWarn
	c.Detail = fmt.Sprintf("%d of %d running outside their own scope (%s): a daemon restart may kill them",
		len(offenders), running, offendersText(offenders))
	c.Fix = "let them finish before restarting relevo.service"
	c.Unsafe = len(offenders)
	return c
}

func restartOK(c Check, detail string) Check {
	c.Severity = SevOK
	c.Detail = detail
	return c
}

func restartCannotTell(c Check) Check {
	c.Severity = SevOK
	c.Detail = "cannot tell on this host"
	return c
}

// offendersText lists at most three offenders, summarising the rest as `and
// K more`.
func offendersText(offenders []string) string {
	const max = 3
	if len(offenders) <= max {
		return strings.Join(offenders, ", ")
	}
	return strings.Join(offenders[:max], ", ") + fmt.Sprintf(", and %d more", len(offenders)-max)
}
