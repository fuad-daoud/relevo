package mastermind

import (
	"fmt"
	"testing"
)

// museTree is a fake process tree: comms names each pid's command, parents
// its parent. Missing pids read as gone.
type museTree struct {
	comms   map[int]string
	parents map[int]int
}

func (m museTree) commOf(pid int) (string, bool) {
	comm, ok := m.comms[pid]
	return comm, ok
}

func (m museTree) ppidOf(pid int) (int, bool) {
	parent, ok := m.parents[pid]
	return parent, ok
}

// TestMuseHostWalk pins the pure ancestor walk: it finds the nearest muse
// ancestor through shells, stops at the bound, and reports not-found when no
// ancestor is muse.
func TestMuseHostWalk(t *testing.T) {
	shells := museTree{
		comms:   map[int]string{100: "bash", 90: "sh", 80: "muse", 1: "init"},
		parents: map[int]int{100: 90, 90: 80, 80: 1},
	}
	if pid, ok := findMuseHost(100, shells.commOf, shells.ppidOf); !ok || pid != 80 {
		t.Errorf("through shells = %d (%v), want 80 (true)", pid, ok)
	}

	direct := museTree{
		comms:   map[int]string{42: "muse", 1: "init"},
		parents: map[int]int{42: 1},
	}
	if pid, ok := findMuseHost(42, direct.commOf, direct.ppidOf); !ok || pid != 42 {
		t.Errorf("direct muse parent = %d (%v), want 42 (true)", pid, ok)
	}

	nearest := museTree{
		comms:   map[int]string{100: "bash", 90: "muse", 80: "muse", 1: "init"},
		parents: map[int]int{100: 90, 90: 80, 80: 1},
	}
	if pid, ok := findMuseHost(100, nearest.commOf, nearest.ppidOf); !ok || pid != 90 {
		t.Errorf("nearest ancestor = %d (%v), want 90 (true)", pid, ok)
	}

	none := museTree{
		comms:   map[int]string{100: "bash", 90: "sh", 1: "init"},
		parents: map[int]int{100: 90, 90: 1},
	}
	if pid, ok := findMuseHost(100, none.commOf, none.ppidOf); ok || pid != 0 {
		t.Errorf("no muse ancestor = %d (%v), want 0 (false)", pid, ok)
	}

	gone := museTree{}
	if pid, ok := findMuseHost(100, gone.commOf, gone.ppidOf); ok || pid != 0 {
		t.Errorf("gone start = %d (%v), want 0 (false)", pid, ok)
	}

	deep := museTree{comms: map[int]string{}, parents: map[int]int{}}
	pid := 1000
	for i := 0; i < museHostDepth+2; i++ {
		deep.comms[pid] = fmt.Sprintf("sh-%d", i)
		deep.parents[pid] = pid - 1
		pid--
	}
	deep.comms[pid] = "muse"
	if got, ok := findMuseHost(1000, deep.commOf, deep.ppidOf); ok || got != 0 {
		t.Errorf("muse past the bound = %d (%v), want 0 (false)", got, ok)
	}
}
