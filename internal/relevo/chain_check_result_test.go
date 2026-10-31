package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestCheckResultWordsFollowTheRunnersResult pins that every settled non-pass a
// runner reports, whatever its exit status, closes a check red for the engine
// and reads red to the reviewer's seed, and that only a pass reads green.
func TestCheckResultWordsFollowTheRunnersResult(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw       string
		wantCheck string
		wantGate  string
	}{
		{"pass", chainCheckGreen, chain.GateGreen},
		{"fail", chainCheckRed, chain.GateRed},
		{"timeout", chainCheckRed, chain.GateRed},
		{"error", chainCheckRed, chain.GateRed},
	}
	for _, c := range cases {
		if got := chainCheckResult(c.raw); got != c.wantCheck {
			t.Errorf("chainCheckResult(%q) = %q, want %q", c.raw, got, c.wantCheck)
		}
		if got := chain.GateResult(&store.GateRecord{Result: c.raw}); got != c.wantGate {
			t.Errorf("chain.GateResult(%q) = %q, want %q", c.raw, got, c.wantGate)
		}
	}
}
