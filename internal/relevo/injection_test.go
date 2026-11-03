package relevo

import (
	"testing"
)

func TestFlaggedParenthetical(t *testing.T) {
	t.Parallel()

	if got := flaggedParenthetical(0); got != "" {
		t.Errorf("(0) = %q, want empty", got)
	}
	if got := flaggedParenthetical(1); got != " (1 instruction-shaped line flagged; see relevo show --log)" {
		t.Errorf("(1) = %q", got)
	}
	if got := flaggedParenthetical(3); got != " (3 instruction-shaped lines flagged; see relevo show --log)" {
		t.Errorf("(3) = %q", got)
	}
}
