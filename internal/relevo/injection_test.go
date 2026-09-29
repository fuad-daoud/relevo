package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestComposeFlagged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		regexLines []int
		kept       []classify.Paragraph
		probs      []float64
		thr        float64
		wantFlag   int
		wantBy     string
		wantAbove  int
		wantMax    float64
	}{
		{
			name:       "no regex, probs [0.1, 0.95, 0.8], thr 0.7",
			regexLines: nil,
			kept: []classify.Paragraph{
				{Line: 1, Lines: 1},
				{Line: 3, Lines: 1},
				{Line: 5, Lines: 1},
			},
			probs:     []float64{0.1, 0.95, 0.8},
			thr:       0.7,
			wantFlag:  2,
			wantBy:    "jev",
			wantAbove: 2,
			wantMax:   0.95,
		},
		{
			name:       "regex line 1 inside paragraph 0 with probs [0.9]",
			regexLines: []int{1},
			kept: []classify.Paragraph{
				{Line: 1, Lines: 2},
			},
			probs:     []float64{0.9},
			thr:       0.7,
			wantFlag:  1,
			wantBy:    "both",
			wantAbove: 1,
			wantMax:   0.9,
		},
		{
			name:       "regex line 1, paragraph 1 at lines 3-4 with p 0.9",
			regexLines: []int{1},
			kept: []classify.Paragraph{
				{Line: 1, Lines: 1},
				{Line: 3, Lines: 2},
			},
			probs:     []float64{0.1, 0.9},
			thr:       0.7,
			wantFlag:  2,
			wantBy:    "both",
			wantAbove: 1,
			wantMax:   0.9,
		},
		{
			name:       "regex only, probs [0.2]",
			regexLines: []int{1},
			kept: []classify.Paragraph{
				{Line: 1, Lines: 1},
			},
			probs:     []float64{0.2},
			thr:       0.7,
			wantFlag:  1,
			wantBy:    "regex",
			wantAbove: 0,
			wantMax:   0.2,
		},
		{
			name:       "nothing",
			regexLines: nil,
			kept: []classify.Paragraph{
				{Line: 1, Lines: 1},
			},
			probs:     []float64{0.2},
			thr:       0.7,
			wantFlag:  0,
			wantBy:    "",
			wantAbove: 0,
			wantMax:   0.2,
		},
		{
			name:       "empty probs",
			regexLines: nil,
			kept:       nil,
			probs:      nil,
			thr:        0.7,
			wantFlag:   0,
			wantBy:     "",
			wantAbove:  0,
			wantMax:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagged, by, above, max := composeFlagged(tc.regexLines, tc.kept, tc.probs, tc.thr)
			if flagged != tc.wantFlag {
				t.Errorf("flagged = %d, want %d", flagged, tc.wantFlag)
			}
			if by != tc.wantBy {
				t.Errorf("by = %q, want %q", by, tc.wantBy)
			}
			if above != tc.wantAbove {
				t.Errorf("above = %d, want %d", above, tc.wantAbove)
			}
			if max != tc.wantMax {
				t.Errorf("max = %v, want %v", max, tc.wantMax)
			}
		})
	}
}

func TestFlaggedParenthetical(t *testing.T) {
	t.Parallel()

	if got := flaggedParenthetical(0, nil); got != "" {
		t.Errorf("(0, nil) = %q, want empty", got)
	}
	if got := flaggedParenthetical(1, nil); got != " (1 instruction-shaped line flagged; see relevo show --log)" {
		t.Errorf("(1, nil) = %q", got)
	}
	if got := flaggedParenthetical(3, nil); got != " (3 instruction-shaped lines flagged; see relevo show --log)" {
		t.Errorf("(3, nil) = %q", got)
	}
	if got := flaggedParenthetical(3, &store.ClassifyRecord{Max: 0.94}); got != " (3 instruction-shaped lines flagged; jev p=0.94; see relevo show --log)" {
		t.Errorf("(3, Max: 0.94) = %q", got)
	}
	if got := flaggedParenthetical(3, &store.ClassifyRecord{Max: 0.94, Note: "classify: timeout after 4s"}); got != " (3 instruction-shaped lines flagged; see relevo show --log)" {
		t.Errorf("(3, Note: timeout) = %q", got)
	}
}
