package chain

import (
	"fmt"
	"slices"
	"testing"
)

// TestPlanFileCandidatesListsDistinctNamesInOrder pins the candidate scan:
// distinct file-like names in order of first appearance, parent traversals
// left out, non-file words ignored.
func TestPlanFileCandidatesListsDistinctNamesInOrder(t *testing.T) {
	t.Parallel()

	texts := [][]byte{
		[]byte("Follow docs/plans/source-plan.md and add internal/new/file.go."),
		[]byte("docs/plans/source-plan.md again, plus hello world."),
		[]byte("See x/../y.go for context."),
	}
	got := PlanFileCandidates(texts)
	if !slices.Equal(got, []string{"docs/plans/source-plan.md", "internal/new/file.go"}) {
		t.Errorf("PlanFileCandidates = %q, want the two distinct names in order", got)
	}
}

// TestPlanFileCandidatesStopsAtTheCap pins the lookup bound: at most 64
// names no matter how many the texts carry.
func TestPlanFileCandidatesStopsAtTheCap(t *testing.T) {
	t.Parallel()

	var texts [][]byte
	for i := 0; i < 80; i++ {
		texts = append(texts, []byte(fmt.Sprintf("read file-%d.go", i)))
	}
	if got := PlanFileCandidates(texts); len(got) != maxPlanFiles {
		t.Errorf("len(PlanFileCandidates) = %d, want %d", len(got), maxPlanFiles)
	}
}
