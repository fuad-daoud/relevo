//go:build unix

package proc

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// InheritedReaper reaps the children a re-exec inherited from the previous
// image; they are still this process's children, and nothing else will ever
// wait for them.
type InheritedReaper struct {
	pids []int
}

// NewInheritedReaper scans procRoot once for processes whose parent is self; it
// must be constructed before this image starts any child. An empty or unreadable
// root falls back to `ps -A -o pid=,ppid=`.
func NewInheritedReaper(self int, procRoot string) *InheritedReaper {
	return &InheritedReaper{pids: scanChildren(self, procRoot)}
}

func scanChildren(self int, procRoot string) []int {
	if procRoot != "" {
		if pids, ok := scanProcRoot(self, procRoot); ok {
			return pids
		}
	}
	return scanPS(self)
}

// scanProcRoot returns (pids, true) when it could read procRoot; (nil, false)
// asks the caller to fall back to ps.
func scanProcRoot(self int, procRoot string) ([]int, bool) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, false
	}

	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue // the process exited between ReadDir and ReadFile
		}
		ppid, ok := ParseProcStatPPID(string(raw))
		if !ok || ppid != self {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, true
}

func scanPS(self int) []int {
	cmd := exec.Command("ps", "-A", "-o", "pid=,ppid=")
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("reaper: ps", "err", err)
		return nil
	}
	// ps runs as a child of this process, so it lists itself with ppid == self
	// and ParsePSChildren puts its own pid in the set. Exclude it: a future
	// child that reuses the pid would otherwise be handed to Wait4 instead of
	// its exec.Cmd.
	psPID := 0
	if cmd.Process != nil {
		psPID = cmd.Process.Pid
	}
	return withoutPID(ParsePSChildren(string(out), self), psPID)
}

// withoutPID returns pids with every occurrence of pid removed.
func withoutPID(pids []int, pid int) []int {
	var kept []int
	for _, p := range pids {
		if p != pid {
			kept = append(kept, p)
		}
	}
	return kept
}

// Reap waits, without blocking, on every inherited pid and returns the pids it
// reaped; a pid it did not inherit is never waited on, so an exec.Cmd's status
// cannot be stolen. A pid is dropped once it is reaped or is no longer ours.
func (r *InheritedReaper) Reap() []int {
	if r == nil {
		return nil
	}

	var reaped, kept []int
	for _, pid := range r.pids {
		gotReaped, drop := reap(pid)
		if gotReaped {
			reaped = append(reaped, pid)
		}
		if !drop {
			kept = append(kept, pid)
		}
	}
	r.pids = kept
	return reaped
}

// reap reports whether pid was reaped and whether it should be dropped. It
// returns (true, true) when Wait4 collected pid, (false, true) when pid is no
// longer ours, and (false, false) while it is still running.
func reap(pid int) (reaped, drop bool) {
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	if wpid == pid {
		return true, true
	}
	if err != nil {
		if !errors.Is(err, syscall.ECHILD) {
			slog.Debug("reaper: wait4", "pid", pid, "err", err)
		}
		return false, true
	}
	return false, false
}
