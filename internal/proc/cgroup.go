package proc

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CgroupRelPath returns pid's cgroup v2 path from /proc/<pid>/cgroup. It
// reports an error when the file is unreadable or carries no v2 line, so a
// v1-only host never reads as an empty path.
func CgroupRelPath(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	rel, ok := parseCgroupV2(data)
	if !ok {
		return "", fmt.Errorf("no cgroup v2 entry for pid %d", pid)
	}
	return rel, nil
}

// parseCgroupV2 returns the path on the "0::" line of /proc/<pid>/cgroup
// content. A v1 line names controllers in the middle field, so only an empty
// middle field with hierarchy 0 is v2.
func parseCgroupV2(data []byte) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" && parts[2] != "" {
			return parts[2], true
		}
	}
	return "", false
}

// MemoryStatAnon returns the anon bytes from <dir>/memory.stat, the same line
// the supervisor sampler reads.
func MemoryStatAnon(dir string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "anon" {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse memory.stat anon %q: %w", fields[1], err)
		}
		return n, nil
	}
	return 0, fmt.Errorf("no anon line in %s", filepath.Join(dir, "memory.stat"))
}

// MemorySwapCurrent returns the bytes in <dir>/memory.swap.current, the same
// file the supervisor sampler cats.
func MemorySwapCurrent(dir string) (int64, error) {
	return readCgroupInt(filepath.Join(dir, "memory.swap.current"))
}

// MemoryCurrent returns the bytes in <dir>/memory.current.
func MemoryCurrent(dir string) (int64, error) {
	return readCgroupInt(filepath.Join(dir, "memory.current"))
}

// MemoryMax returns the limit in <dir>/memory.max. The literal "max" reads as
// limited=false, so an unlimited cgroup never reads as a zero limit.
func MemoryMax(dir string) (limit int64, limited bool, err error) {
	path := filepath.Join(dir, "memory.max")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(string(data)) == "max" {
		return 0, false, nil
	}
	limit, err = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return limit, true, nil
}

// MemoryPressureSomeAvg10 returns the some line's avg10 from
// <dir>/memory.pressure.
func MemoryPressureSomeAvg10(dir string) (float64, error) {
	data, err := os.ReadFile(filepath.Join(dir, "memory.pressure"))
	if err != nil {
		return 0, err
	}
	return parsePressureSomeAvg10(data)
}

// parsePressureSomeAvg10 reads avg10 off the some line alone. The full line
// carries an independent counter for the same window, so scanning every line
// for avg10 would mix two stalls.
func parsePressureSomeAvg10(data []byte) (float64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "some" {
			continue
		}
		for _, field := range fields[1:] {
			name, value, found := strings.Cut(field, "=")
			if found && name == "avg10" {
				avg, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return 0, fmt.Errorf("parse some avg10 %q: %w", value, err)
				}
				return avg, nil
			}
		}
		return 0, fmt.Errorf("some line carries no avg10: %q", line)
	}
	return 0, fmt.Errorf("no some line in memory.pressure")
}

// MemTotal returns machine memory in bytes from /proc/meminfo.
func MemTotal() (int64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	return parseMemTotal(data)
}

// parseMemTotal reads MemTotal's kB figure and scales it to bytes. The kernel
// reports meminfo in kB, so a bare number is never bytes already.
func parseMemTotal(data []byte) (int64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "MemTotal:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, fmt.Errorf("malformed MemTotal line %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemTotal %q: %w", fields[1], err)
		}
		if kb > math.MaxInt64/1024 {
			return 0, fmt.Errorf("MemTotal %q overflows bytes", fields[1])
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("no MemTotal line in meminfo")
}

// readCgroupInt returns the single integer a cgroup file holds. A missing or
// malformed file is an error, never a zero reading.
func readCgroupInt(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return n, nil
}
