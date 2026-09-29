package spawn

import (
	"testing"
)

// TestGoMaxProcsFor pins the rule: the CPUs a launched scope allows, or ok
// false when it limits nothing. A malformed AllowedCPUs or CPUQuota
// contributes nothing rather than panicking.
func TestGoMaxProcsFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		scope ScopeSpec
		want  int
		ok    bool
	}{
		{"no limits", ScopeSpec{}, 0, false},
		{"single cpu pin", ScopeSpec{AllowedCPUs: "2"}, 1, true},
		{"cpu range pin", ScopeSpec{AllowedCPUs: "0-2"}, 3, true},
		{"cpu list pin", ScopeSpec{AllowedCPUs: "0,2-3"}, 3, true},
		{"quota two cores", ScopeSpec{CPUQuota: "200%"}, 2, true},
		{"quota rounds up", ScopeSpec{CPUQuota: "150%"}, 2, true},
		{"quota below one core", ScopeSpec{CPUQuota: "50%"}, 1, true},
		{"quota wider than the pin", ScopeSpec{CPUQuota: "300%", AllowedCPUs: "1"}, 1, true},
		{"pin wider than the quota", ScopeSpec{CPUQuota: "100%", AllowedCPUs: "0-3"}, 1, true},
		{"malformed quota", ScopeSpec{CPUQuota: "abc"}, 0, false},
		{"malformed cpu list", ScopeSpec{AllowedCPUs: "x"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := GoMaxProcsFor(tc.scope)
			if got != tc.want || ok != tc.ok {
				t.Errorf("GoMaxProcsFor(%+v) = %d, %v; want %d, %v", tc.scope, got, ok, tc.want, tc.ok)
			}
		})
	}
}
