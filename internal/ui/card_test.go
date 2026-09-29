package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestCardRowNeverOverflows verifies that renderCard(60, …) with a 200-cell row
// cuts lines to at most 60 wide and keeps the right border intact.
func TestCardRowNeverOverflows(t *testing.T) {
	longRow := strings.Repeat("x", 200)
	lines := renderCard(60, "title", "", []string{longRow})
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("line %d width = %d, want <= 60", i, w)
		}
	}
	stripped := strings.TrimRight(stripANSI(lines[1]), " ")
	if !strings.HasSuffix(stripped, "│") {
		t.Errorf("second line stripped %q does not end with │", stripped)
	}
}
