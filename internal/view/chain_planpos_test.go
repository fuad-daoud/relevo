package view

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestChainFlowPlanPosExhausted pins the helper itself, so the two status
// surfaces it serves cannot drift apart: an exhausted walk reads its last plan,
// an in-flight walk reads its own index, and an empty plan input clamps to 1.
func TestChainFlowPlanPosExhausted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		it         workflow.Iter
		pos, total int
	}{
		{"exhausted", workflow.Iter{Index: -1, Items: []string{"a", "b", "c"}, Done: true}, 3, 3},
		{"mid-walk", workflow.Iter{Index: 1, Items: []string{"a", "b", "c"}}, 2, 3},
		{"before the first item", workflow.Iter{Index: -1, Items: []string{"a", "b", "c"}}, 1, 3},
		{"no plans", workflow.Iter{Index: -1, Done: true}, 1, 0},
		{"exhausted with no items", workflow.Iter{Index: -1, Done: true}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := workflow.State{Iter: map[string]workflow.Iter{"plans": tc.it}}
			pos, total := FlowPlanPos(st)
			if pos != tc.pos || total != tc.total {
				t.Errorf("FlowPlanPos = %d/%d, want %d/%d", pos, total, tc.pos, tc.total)
			}
		})
	}
}
