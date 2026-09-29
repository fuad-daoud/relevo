package mastermind

import (
	"strings"
	"testing"
)

// TestRunnerEnvEntry pins the marker's one spelling: "RELEVO_RUNNER=<name>",
// which IsRunner reads as a runner.
func TestRunnerEnvEntry(t *testing.T) {
	t.Parallel()

	entry := RunnerEnvEntry("webshop")
	if entry != "RELEVO_RUNNER=webshop" {
		t.Errorf("RunnerEnvEntry = %q, want RELEVO_RUNNER=webshop", entry)
	}

	kv := strings.SplitN(entry, "=", 2)
	if len(kv) != 2 {
		t.Fatalf("entry %q has no =", entry)
	}
	env := func(k string) string {
		if k == kv[0] {
			return kv[1]
		}
		return ""
	}
	if !IsRunner(env) {
		t.Errorf("IsRunner for %q = false, want true", entry)
	}
}

// TestIsRunner pins the marker rule: only a non-empty RELEVO_RUNNER makes a
// process a runner, and a nil env reads the safe answer -- false.
func TestIsRunner(t *testing.T) {
	t.Parallel()

	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}

	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, false},
		{"unset", env(map[string]string{}), false},
		{"empty value", env(map[string]string{RunnerEnv: ""}), false},
		{"set", env(map[string]string{RunnerEnv: "api"}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRunner(tc.env); got != tc.want {
				t.Errorf("IsRunner = %v, want %v", got, tc.want)
			}
		})
	}
}
