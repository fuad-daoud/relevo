package proc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCgroupV2(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
		ok   bool
	}{
		{"v2 only", "0::/user.slice/user-1000.slice/session-3.scope\n", "/user.slice/user-1000.slice/session-3.scope", true},
		{"v1 and v2", "2:cpu,cpuacct:/user.slice/user-1000.slice\n0::/user.slice/user-1000.slice/session-3.scope\n", "/user.slice/user-1000.slice/session-3.scope", true},
		{"v2 first", "0::/relevo.slice/relevo-round-abc.scope\n1:name=systemd:/relevo.slice\n", "/relevo.slice/relevo-round-abc.scope", true},
		{"v1 only", "2:cpu,cpuacct:/user.slice\n1:name=systemd:/user.slice\n", "", false},
		{"empty", "", "", false},
		{"malformed lines skipped", "not-a-cgroup-line\n0::/real/path\n", "/real/path", true},
		{"hierarchy 1 with empty controllers is not v2", "1::/user.slice\n", "", false},
		{"hierarchy 0 with controllers is not v2", "0:cpu:/user.slice\n", "", false},
		{"empty path is no path", "0::\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseCgroupV2([]byte(tc.data))
			if got != tc.want || ok != tc.ok {
				t.Errorf("parseCgroupV2(%q) = %q, %v; want %q, %v", tc.data, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCgroupRelPath(t *testing.T) {
	if _, err := CgroupRelPath(-1); err == nil {
		t.Error("CgroupRelPath(-1) = nil error; want an error for an unreadable cgroup file")
	}

	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup on this platform")
	}
	want, ok := parseCgroupV2(data)
	if !ok {
		t.Skip("no cgroup v2 entry for this test process")
	}
	got, err := CgroupRelPath(os.Getpid())
	if err != nil {
		t.Fatalf("CgroupRelPath(self) = %v; want the v2 path", err)
	}
	if got != want {
		t.Errorf("CgroupRelPath(self) = %q, want %q", got, want)
	}
}

func writeCgroupFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMemoryStatAnon(t *testing.T) {
	dir := writeCgroupFile(t, "memory.stat", "anon 1048576\nfile 2097152\nkernel 65536\n")
	if got, err := MemoryStatAnon(dir); err != nil || got != 1048576 {
		t.Errorf("MemoryStatAnon = %d, %v; want 1048576, nil", got, err)
	}

	if _, err := MemoryStatAnon(t.TempDir()); err == nil {
		t.Error("MemoryStatAnon on a missing memory.stat = nil error; want an error, never a zero")
	}

	bare := writeCgroupFile(t, "memory.stat", "file 2097152\nkernel 65536\n")
	if _, err := MemoryStatAnon(bare); err == nil {
		t.Error("MemoryStatAnon without an anon line = nil error; want an error, never a zero")
	}

	bad := writeCgroupFile(t, "memory.stat", "anon twelve\n")
	if _, err := MemoryStatAnon(bad); err == nil {
		t.Error("MemoryStatAnon with a malformed anon line = nil error; want an error")
	}
}

func TestMemorySwapCurrent(t *testing.T) {
	dir := writeCgroupFile(t, "memory.swap.current", "33554432\n")
	if got, err := MemorySwapCurrent(dir); err != nil || got != 33554432 {
		t.Errorf("MemorySwapCurrent = %d, %v; want 33554432, nil", got, err)
	}

	if _, err := MemorySwapCurrent(t.TempDir()); err == nil {
		t.Error("MemorySwapCurrent on a missing file = nil error; want an error, never a zero")
	}

	bad := writeCgroupFile(t, "memory.swap.current", "lots\n")
	if _, err := MemorySwapCurrent(bad); err == nil {
		t.Error("MemorySwapCurrent with a malformed file = nil error; want an error")
	}
}

func TestMemoryCurrent(t *testing.T) {
	dir := writeCgroupFile(t, "memory.current", "134217728\n")
	if got, err := MemoryCurrent(dir); err != nil || got != 134217728 {
		t.Errorf("MemoryCurrent = %d, %v; want 134217728, nil", got, err)
	}

	if _, err := MemoryCurrent(t.TempDir()); err == nil {
		t.Error("MemoryCurrent on a missing file = nil error; want an error, never a zero")
	}

	bad := writeCgroupFile(t, "memory.current", "")
	if _, err := MemoryCurrent(bad); err == nil {
		t.Error("MemoryCurrent with an empty file = nil error; want an error")
	}
}

func TestMemoryMax(t *testing.T) {
	capped := writeCgroupFile(t, "memory.max", "1073741824\n")
	got, limited, err := MemoryMax(capped)
	if err != nil || !limited || got != 1073741824 {
		t.Errorf("MemoryMax(capped) = %d, %v, %v; want 1073741824, true, nil", got, limited, err)
	}

	open := writeCgroupFile(t, "memory.max", "max\n")
	got, limited, err = MemoryMax(open)
	if err != nil || limited || got != 0 {
		t.Errorf("MemoryMax(max) = %d, %v, %v; want 0, false, nil: max is unlimited, not zero", got, limited, err)
	}

	if _, _, err := MemoryMax(t.TempDir()); err == nil {
		t.Error("MemoryMax on a missing file = nil error; want an error")
	}

	bad := writeCgroupFile(t, "memory.max", "a lot\n")
	if _, _, err := MemoryMax(bad); err == nil {
		t.Error("MemoryMax with a malformed file = nil error; want an error")
	}
}

// TestMemoryPressureSomeAvg10ReadsItsOwnLine pins that the reader takes avg10
// off the some line even when the full line disagrees: the two lines carry
// independent counters for the same window.
func TestMemoryPressureSomeAvg10ReadsItsOwnLine(t *testing.T) {
	dir := writeCgroupFile(t, "memory.pressure", "some avg10=1.23 avg60=0.45 avg300=0.00 total=111\nfull avg10=9.99 avg60=0.00 avg300=0.00 total=222\n")
	got, err := MemoryPressureSomeAvg10(dir)
	if err != nil || got != 1.23 {
		t.Errorf("MemoryPressureSomeAvg10 = %v, %v; want 1.23, nil: the full line's 9.99 must not leak in", got, err)
	}

	if _, err := MemoryPressureSomeAvg10(t.TempDir()); err == nil {
		t.Error("MemoryPressureSomeAvg10 on a missing file = nil error; want an error, never a zero")
	}

	fullOnly := writeCgroupFile(t, "memory.pressure", "full avg10=0.00 avg60=0.00 avg300=0.00 total=222\n")
	if _, err := MemoryPressureSomeAvg10(fullOnly); err == nil {
		t.Error("MemoryPressureSomeAvg10 with only a full line = nil error; want an error")
	}

	bad := writeCgroupFile(t, "memory.pressure", "some avg10=lots avg60=0.00 avg300=0.00 total=111\n")
	if _, err := MemoryPressureSomeAvg10(bad); err == nil {
		t.Error("MemoryPressureSomeAvg10 with a malformed avg10 = nil error; want an error")
	}

	noAvg := writeCgroupFile(t, "memory.pressure", "some avg60=0.45 avg300=0.00 total=111\n")
	if _, err := MemoryPressureSomeAvg10(noAvg); err == nil {
		t.Error("MemoryPressureSomeAvg10 with a some line lacking avg10 = nil error; want an error")
	}
}

func TestParseMemTotal(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want int64
		ok   bool
	}{
		{"plain", "MemTotal:        4024548 kB\nMemFree: 123456 kB\n", 4024548 * 1024, true},
		{"tight spacing", "MemTotal: 16384 kB\n", 16384 * 1024, true},
		{"without the key", "MemFree: 123456 kB\n", 0, false},
		{"empty", "", 0, false},
		{"malformed number", "MemTotal: lots kB\n", 0, false},
		{"missing unit", "MemTotal: 4024548\n", 0, false},
		{"wrong unit", "MemTotal: 4024548 MB\n", 0, false},
		{"overflows bytes", "MemTotal: 9007199254740992 kB\n", 0, false},
		{"exact overflow boundary", "MemTotal: 9007199254740991 kB\n", 9007199254740991 * 1024, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMemTotal([]byte(tc.data))
			if tc.ok {
				if err != nil || got != tc.want {
					t.Errorf("parseMemTotal = %d, %v; want %d, nil", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Errorf("parseMemTotal(%q) = %d, nil; want an error, never a zero", tc.data, got)
			}
		})
	}
}

func TestMemTotal(t *testing.T) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Skip("no /proc/meminfo on this platform")
	}
	want, err := parseMemTotal(data)
	if err != nil {
		t.Skipf("this host's meminfo has no MemTotal line: %v", err)
	}
	got, err := MemTotal()
	if err != nil {
		t.Fatalf("MemTotal = %v; want the parsed value", err)
	}
	if got != want {
		t.Errorf("MemTotal = %d, want %d from this host's meminfo", got, want)
	}
}
