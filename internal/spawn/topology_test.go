package spawn

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeSysFile plants one fake sysfs file under the temp root.
func writeSysFile(t *testing.T, root, cpu, rel, body string) {
	t.Helper()
	dir := filepath.Join(root, cpu, filepath.Dir(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(root, cpu, rel), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", rel, err)
	}
}

// TestReadCPUTopologyFakeTree pins the sysfs read: sibling lists parse in
// both range and comma form, while each broken file degrades alone — a CPU
// with no topology file or a malformed sibling list runs alone with its
// speed still known, and a CPU with no cpufreq directory or a malformed
// frequency runs unknown with its siblings still parsed. A missing root
// reads as no entries rather than failing.
func TestReadCPUTopologyFakeTree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSysFile(t, root, "cpu0", filepath.Join("topology", "thread_siblings_list"), "0-1\n")
	writeSysFile(t, root, "cpu0", filepath.Join("cpufreq", "cpuinfo_max_freq"), "4400000\n")
	writeSysFile(t, root, "cpu1", filepath.Join("topology", "thread_siblings_list"), "0-1\n")
	writeSysFile(t, root, "cpu1", filepath.Join("cpufreq", "cpuinfo_max_freq"), "4400000\n")
	// No topology file: alone, speed still known.
	writeSysFile(t, root, "cpu2", filepath.Join("cpufreq", "cpuinfo_max_freq"), "3300000\n")
	// No cpufreq directory: siblings still parse, speed unknown.
	writeSysFile(t, root, "cpu3", filepath.Join("topology", "thread_siblings_list"), "3\n")
	// Malformed frequency: siblings still parse, speed unknown.
	writeSysFile(t, root, "cpu4", filepath.Join("topology", "thread_siblings_list"), "0,4\n")
	writeSysFile(t, root, "cpu4", filepath.Join("cpufreq", "cpuinfo_max_freq"), "lots\n")
	// Malformed sibling list: alone, speed still known.
	writeSysFile(t, root, "cpu5", filepath.Join("topology", "thread_siblings_list"), "oops\n")
	writeSysFile(t, root, "cpu5", filepath.Join("cpufreq", "cpuinfo_max_freq"), "3300000\n")

	got := ReadCPUTopology(root, []int{0, 1, 2, 3, 4, 5})

	wantSiblings := map[int][]int{0: {0, 1}, 1: {0, 1}, 3: {3}, 4: {0, 4}}
	if !reflect.DeepEqual(got.Siblings, wantSiblings) {
		t.Errorf("Siblings = %v, want %v", got.Siblings, wantSiblings)
	}
	wantSpeed := map[int]int{0: 4400000, 1: 4400000, 2: 3300000, 5: 3300000}
	if !reflect.DeepEqual(got.MaxFreqKHz, wantSpeed) {
		t.Errorf("MaxFreqKHz = %v, want %v", got.MaxFreqKHz, wantSpeed)
	}

	if empty := ReadCPUTopology(filepath.Join(root, "absent"), []int{0}); len(empty.Siblings) != 0 || len(empty.MaxFreqKHz) != 0 {
		t.Errorf("a missing root read as %v, want no entries", empty)
	}
}
