//go:build unix

package proc

import (
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// TestWithoutPID covers the pure exclusion of ps's own pid.
func TestWithoutPID(t *testing.T) {
	tests := []struct {
		name string
		pids []int
		pid  int
		want []int
	}{
		{"removes a match", []int{4, 7, 9}, 7, []int{4, 9}},
		{"removes every match", []int{7, 4, 7}, 7, []int{4}},
		{"leaves a non-member alone", []int{4, 9}, 7, []int{4, 9}},
		{"empty set", nil, 7, nil},
		{"removes the only pid", []int{7}, 7, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withoutPID(tt.pids, tt.pid); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("withoutPID(%v, %d) = %v, want %v", tt.pids, tt.pid, got, tt.want)
			}
		})
	}
}

// TestInheritedReaperDropsANonChild covers the drop half of the contract: a pid
// that is not our child is never reported as reaped, so a second Reap has
// nothing left to try.
func TestInheritedReaperDropsANonChild(t *testing.T) {
	r := &InheritedReaper{pids: []int{os.Getppid()}}

	if reaped := r.Reap(); len(reaped) != 0 {
		t.Errorf("Reap() = %v, want nothing for a pid that is not our child", reaped)
	}
	if again := r.Reap(); len(again) != 0 {
		t.Errorf("second Reap() = %v, want nothing because the pid was dropped", again)
	}
}

// TestScanChildrenPSExcludesItself runs the darwin path on Linux: an empty
// procRoot forces scanPS, which starts ps as a child of this process.
func TestScanChildrenPSExcludesItself(t *testing.T) {
	if got := scanChildren(os.Getpid(), ""); len(got) != 0 {
		t.Errorf("scanChildren(self, \"\") = %v, want nothing (ps must not list itself into the set)", got)
	}
}

// TestInheritedReaperReapsAnInheritedChild covers a child started before the
// reaper is constructed; Reap must collect it, once.
func TestInheritedReaperReapsAnInheritedChild(t *testing.T) {
	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid

	r := NewInheritedReaper(os.Getpid(), "/proc")
	time.Sleep(400 * time.Millisecond)

	reaped := r.Reap()
	if !containsPID(reaped, pid) {
		t.Fatalf("Reap() = %v, want it to contain the inherited pid %d", reaped, pid)
	}
	// The pid was reaped, so a second Reap must not report it again.
	if again := r.Reap(); containsPID(again, pid) {
		t.Errorf("Reap() returned %d twice: %v", pid, again)
	}
}

// TestInheritedReaperLeavesNewChildrenAlone is the safety half: a child started
// after construction is not in the inherited set, so Reap must not wait on it.
func TestInheritedReaperLeavesNewChildrenAlone(t *testing.T) {
	r := NewInheritedReaper(os.Getpid(), "/proc")

	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if reaped := r.Reap(); len(reaped) != 0 {
		t.Errorf("Reap() = %v, want nothing for a child started after construction", reaped)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait() after Reap: %v (the reaper must not steal a new child's status)", err)
	}
}

func containsPID(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}
