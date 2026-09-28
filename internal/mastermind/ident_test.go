package mastermind

import (
	"testing"
)

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
