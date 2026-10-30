package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// rail_test.go survives only for the shared fixtures and the one rail
// helper the fleet still uses. The card, compact, facts, grouping,
// gutter-focus and railWindow tests are deleted (X2); the archived-row
// tests are deleted (X3). The stale-label and whatAge assertions ported to
// view_fleet_test.go.

// railNow is in the local zone on purpose: the ui formats clocks with
// .Local(), and a UTC fixture would make "13:02" depend on the machine.
var railNow = time.Date(2026, 9, 17, 14, 2, 0, 0, time.Local)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// plain strips ANSI and collapses runs of spaces, so an assertion pins the
// words and their order, not the padding widget.Fit() adds.
func plain(s string) string { return strings.Join(strings.Fields(stripANSI(s)), " ") }

// TestClipName keeps the NAME column's truncation: it fits, clips with an
// ellipsis to exactly the width, and leaves an exact fit alone.
func TestClipName(t *testing.T) {
	if got := clipName("webshop", 10); got != "webshop" {
		t.Errorf("fits: %q", got)
	}
	if got := clipName("spaceapi-ingest", 10); got != "spaceapi-…" || lipgloss.Width(got) != 10 {
		t.Errorf("clipped: %q (%d)", got, lipgloss.Width(got))
	}
	if got := clipName("ab", 2); got != "ab" {
		t.Errorf("exact fit: %q", got)
	}
}
