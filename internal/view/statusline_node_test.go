package view

import (
	"testing"
)

// TestDropServerToFitLeavesOtherTextAlone pins that only the row's own builder
// name is rewritten: a chain row carries no name, and a name with no node has
// nothing to protect.
func TestDropServerToFitLeavesOtherTextAlone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, mid, on string
	}{
		{"chain row", "chain c1 · plans 1/2 · build@head→b13 long text", ""},
		{"no node", "r1 · builder on muse-xhigh@head and more", "muse-xhigh@head"},
		{"no server", "r1 · builder on muse-xhigh and more", "muse-xhigh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := dropServerToFit(tt.mid, tt.on, 5); got != tt.mid {
				t.Errorf("dropServerToFit = %q, want it unchanged", got)
			}
		})
	}
}
