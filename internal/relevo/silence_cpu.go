package relevo

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// processTreeCPUTicks sums utime+stime, in clock ticks, over pid and all its
// descendants. ok is false off Linux or when any read fails.
func processTreeCPUTicks(pid int) (ticks uint64, ok bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	return processTreeCPUTicksIn("/proc", pid)
}

type procStat struct{ ppid, ticks uint64 }

func processTreeCPUTicksIn(root string, pid int) (uint64, bool) {
	paths, err := filepath.Glob(filepath.Join(root, "[0-9]*", "stat"))
	if err != nil {
		return 0, false
	}
	procs := map[uint64]procStat{}
	kids := map[uint64][]uint64{}
	for _, p := range paths {
		id, err := strconv.ParseUint(filepath.Base(filepath.Dir(p)), 10, 64)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue // the process exited between the glob and the read
		}
		ps, ok := parseProcStat(string(raw))
		if !ok {
			return 0, false
		}
		procs[id] = ps
		kids[ps.ppid] = append(kids[ps.ppid], id)
	}
	if _, ok := procs[uint64(pid)]; !ok {
		return 0, false
	}
	var sum uint64
	todo := []uint64{uint64(pid)}
	for len(todo) > 0 {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		sum += procs[id].ticks
		todo = append(todo, kids[id]...)
	}
	return sum, true
}

// parseProcStat reads ppid, utime and stime from a /proc/<pid>/stat line. The
// comm field may hold spaces and parentheses, so the fields start after the
// last ')'.
func parseProcStat(line string) (procStat, bool) {
	i := strings.LastIndexByte(line, ')')
	if i < 0 {
		return procStat{}, false
	}
	f := strings.Fields(line[i+1:])
	// f[0] is state (field 3), so ppid is f[1], utime f[11], stime f[12].
	if len(f) < 13 {
		return procStat{}, false
	}
	ppid, e1 := strconv.ParseUint(f[1], 10, 64)
	ut, e2 := strconv.ParseUint(f[11], 10, 64)
	st, e3 := strconv.ParseUint(f[12], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return procStat{}, false
	}
	return procStat{ppid: ppid, ticks: ut + st}, true
}
