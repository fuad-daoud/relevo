package proc

import (
	"reflect"
	"slices"
	"testing"
)

// TestGitNoFsmonitorEnv pins which index the entry takes and the count that
// makes git read it: the count already in force, extra first, then parent, with
// a non-numeric count as 0. Inputs are never mutated.
func TestGitNoFsmonitorEnv(t *testing.T) {
	cases := []struct {
		name   string
		parent []string
		extra  []string
		want   []string
	}{
		{
			name: "no count anywhere",
			want: []string{"GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=1"},
		},
		{
			name:   "count 2 in the parent",
			parent: []string{"PATH=/bin", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=user.name"},
			want:   []string{"GIT_CONFIG_KEY_2=core.fsmonitor", "GIT_CONFIG_VALUE_2=false", "GIT_CONFIG_COUNT=3"},
		},
		{
			name:   "count 1 in extra overrides a parent 3",
			parent: []string{"GIT_CONFIG_COUNT=3"},
			extra:  []string{"GIT_CONFIG_COUNT=1"},
			want:   []string{"GIT_CONFIG_KEY_1=core.fsmonitor", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_COUNT=2"},
		},
		{
			name:   "non-numeric count is treated as 0",
			parent: []string{"GIT_CONFIG_COUNT=two"},
			want:   []string{"GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=1"},
		},
		{
			name:   "bare count in extra is treated as 0",
			parent: []string{"GIT_CONFIG_COUNT=4"},
			extra:  []string{"GIT_CONFIG_COUNT"},
			want:   []string{"GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parentCopy := slices.Clone(tc.parent)
			extraCopy := slices.Clone(tc.extra)

			got := gitNoFsmonitorEnv(tc.parent, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("gitNoFsmonitorEnv() = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.parent, parentCopy) {
				t.Errorf("parent was mutated: got %v, want %v", tc.parent, parentCopy)
			}
			if !reflect.DeepEqual(tc.extra, extraCopy) {
				t.Errorf("extra was mutated: got %v, want %v", tc.extra, extraCopy)
			}
		})
	}
}
