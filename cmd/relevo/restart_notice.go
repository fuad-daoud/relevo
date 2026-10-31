package main

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// restartNotice is the line `relevo status` prints above the rows when a daemon
// restart right now would kill a process running outside its own scope, and ""
// whenever the restart check is not a warning. It is pure: every input is
// the check itself, so it is table-tested without a store or a process. The
// count comes from Check.Unsafe, never from parsing Detail.
func restartNotice(c doctor.Check) string {
	if c.Severity != doctor.SevWarn {
		return ""
	}
	return fmt.Sprintf("restart unsafe: %d running outside their own scope -- relevo doctor", c.Unsafe)
}

// readCgroup reads the cgroup file of pid on this host. RestartSafety reads it
// per running process; where it does not exist -- any host without /proc --
// the read fails and the check says it cannot tell.
func readCgroup(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	return string(raw), err
}

// restartCheck is the RestartSafety row for rt's local bindings: the same
// computation `relevo doctor` and `relevo status` both use, cheap enough for both
// (one small file read per running process). A store read error yields no
// processes, so the row degrades to a quiet OK and never fails either command.
func restartCheck(rt relevo.Runtime) doctor.Check {
	var bindings []store.Binding
	if rt.Store != nil {
		if bs, err := rt.Store.List(); err == nil {
			bindings = bs
		}
	}
	return doctor.RestartSafety(toRunningProcs(housekeep.RunningProcs(bindings)), readCgroup)
}

// toRunningProcs converts the collector's relevo-agnostic tuples to the doctor's
// type, which is why internal/relevo and internal/doctor need not import each
// other.
func toRunningProcs(refs []housekeep.RunningProcRef) []doctor.RunningProc {
	out := make([]doctor.RunningProc, 0, len(refs))
	for _, r := range refs {
		out = append(out, doctor.RunningProc{Binding: r.Binding, Kind: r.Kind, PID: r.PID})
	}
	return out
}

// insertRestartRow places the restart row directly after the daemon row.
// Where there is no daemon row it falls back to insertGlobalCheck's
// after-the-globals position.
func insertRestartRow(checks []doctor.Check, c doctor.Check) []doctor.Check {
	for i, existing := range checks {
		if existing.Group == "" && existing.Name == "daemon" {
			out := make([]doctor.Check, 0, len(checks)+1)
			out = append(out, checks[:i+1]...)
			out = append(out, c)
			return append(out, checks[i+1:]...)
		}
	}
	return doctor.InsertGlobalCheck(checks, c)
}
