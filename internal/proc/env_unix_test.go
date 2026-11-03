//go:build unix

package proc

import (
	"slices"
	"testing"
)

// TestSpawnEnvAppliesTmpDirDefaults pins that the runner's directory reaches
// the child through spawnEnv, that it is the only copy of each name, and that
// an inherited value and a spec entry each keep their own.
func TestSpawnEnvAppliesTmpDirDefaults(t *testing.T) {
	got := spawnEnv([]string{"HOME=/root", "TMPDIR=", "KEEP=/user/tmp"}, []string{"GOTMPDIR=/spec/gotmp"},
		nil, nil, "", "/state/tmp")

	want := []string{"TMPDIR=/state/tmp", "GOTMPDIR=/spec/gotmp", "KEEP=/user/tmp"}
	for _, w := range want {
		if count := countEnvName(got, w); count != 1 {
			t.Errorf("%q appears %d times in %v; want exactly one", w, count, got)
		}
	}
	if countEnvName(got, "TMPDIR=") != 0 {
		t.Errorf("the empty inherited TMPDIR survived beside the default: %v", got)
	}
	if !slices.Contains(got, "HOME=/root") {
		t.Errorf("spawnEnv dropped an unrelated parent entry: %v", got)
	}
}

// countEnvName counts the entries equal to want.
func countEnvName(env []string, want string) int {
	n := 0
	for _, e := range env {
		if e == want {
			n++
		}
	}
	return n
}
