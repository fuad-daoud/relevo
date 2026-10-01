package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
)

// TestFormatAccountPools pins policy pool view: each group lists its
// accounts in config order with any live gate, and an unconfigured pool
// renders nothing at all so a host without accounts prints the view it always
// did.
func TestFormatAccountPools(t *testing.T) {
	t.Parallel()

	set := account.Set{
		{Name: "cp1", Harness: account.OpenCode, Groups: []string{"cline-pass"}, Integration: "cline-pass", Label: "C1"},
		{Name: "cp2", Harness: account.OpenCode, Groups: []string{"cline-pass"}, Integration: "cline-pass", Label: "C2"},
		{Name: "work", Harness: account.Claude, Groups: []string{"anthropic"}, ConfigDir: "/home/u/.claude-work"},
	}
	gates := []availability.Gate{{Token: "anthropic@work", Kind: availability.RolesMissing}}
	live := []availability.Entry{{Kind: availability.RateLimited, Subject: "cline-pass@cp1", Until: baseTime.Add(time.Hour)}}

	got := formatAccountPools(set, gates, live)
	for _, want := range []string{
		"\naccounts\n",
		"  cline-pass\n",
		"    cp1", "rate-limited until",
		"    cp2", "ready",
		"  anthropic\n",
		"    work", "agents missing until cleared",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pool view missing %q:\n%s", want, got)
		}
	}

	if got := formatAccountPools(nil, nil, nil); got != "" {
		t.Errorf("no accounts must render nothing, got %q", got)
	}
}
