package relevo

import "testing"

// TestRepoSettingsTrimpathOn pins the effective value off one method: unset
// and on both read on, and only the stored refusal opts a repository out.
func TestRepoSettingsTrimpathOn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		stored string
		want   bool
	}{
		{"", true},
		{"on", true},
		{"off", false},
	} {
		if got := (RepoSettings{Trimpath: tc.stored}).TrimpathOn(); got != tc.want {
			t.Errorf("TrimpathOn(%q) = %v, want %v", tc.stored, got, tc.want)
		}
	}
}
