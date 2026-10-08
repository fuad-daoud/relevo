package sync

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestRunnerEnabledReportsWhetherSyncIsWired pins the holder's one decision: a
// runner drives only when it holds both a log transport and a machine-local
// file, so a trigger that finds either missing skips rather than queueing work
// that can only fail.
func TestRunnerEnabledReportsWhetherSyncIsWired(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	client := synclog.NewMemTransport("origin-a")
	cases := map[string]struct {
		runner *Runner
		want   bool
	}{
		"a wired runner":              {runner: &Runner{Client: client, Local: local}, want: true},
		"a runner with no client":     {runner: &Runner{Local: local}, want: false},
		"a runner with no local file": {runner: &Runner{Client: client}, want: false},
		"a nil runner":                {runner: nil, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.runner.Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
