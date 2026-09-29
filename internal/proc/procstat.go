package proc

import (
	"strconv"
	"strings"
)

// ParseProcStatPPID reads the parent pid out of one /proc/<pid>/stat line; the
// command name is parenthesised and may contain spaces and parentheses, so the
// fields are counted from after the last ')'.
func ParseProcStatPPID(stat string) (ppid int, ok bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}

	fields := strings.Fields(stat[i+1:])
	if len(fields) < 2 {
		return 0, false
	}

	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// ParsePSChildren returns every pid in `ps -A -o pid=,ppid=` output whose parent
// is self, in ps's order; it is the fallback for a host with no readable root.
func ParsePSChildren(out string, self int) []int {
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if ppid == self {
			pids = append(pids, pid)
		}
	}
	return pids
}
