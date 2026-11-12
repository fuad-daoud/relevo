package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// CPUTopology carries the per-CPU placement hints the round picker spreads
// by: each CPU's sibling threads, and its max frequency in kHz. A CPU with
// no entry runs alone at unknown speed.
type CPUTopology struct {
	Siblings   map[int][]int
	MaxFreqKHz map[int]int
}

// ReadCPUTopology reads each listed CPU's sibling list and max frequency
// from the sysfs tree under root. A missing, unreadable or malformed file
// leaves that CPU alone at unknown speed; the read never fails, so a
// non-Linux host without sysfs simply yields no entries. Pure apart from
// the reads, and small: rounds start rarely.
func ReadCPUTopology(root string, cpus []int) CPUTopology {
	topo := CPUTopology{Siblings: make(map[int][]int, len(cpus)), MaxFreqKHz: make(map[int]int, len(cpus))}
	for _, c := range cpus {
		base := filepath.Join(root, "cpu"+strconv.Itoa(c))
		if raw, err := os.ReadFile(filepath.Join(base, "topology", "thread_siblings_list")); err == nil {
			// A sibling file names its own CPU, but only the parse
			// decides: re-add a missing self so the group is whole.
			if list, err := ParseCPUList(strings.TrimSpace(string(raw))); err == nil {
				if !slices.Contains(list, c) {
					list = append(list, c)
					slices.Sort(list)
				}
				topo.Siblings[c] = list
			}
		}
		if raw, err := os.ReadFile(filepath.Join(base, "cpufreq", "cpuinfo_max_freq")); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && v >= 0 {
				topo.MaxFreqKHz[c] = v
			}
		}
	}
	return topo
}
