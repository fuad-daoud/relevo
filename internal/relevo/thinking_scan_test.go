package relevo

import (
	"regexp"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
)

func TestMatchLimitSkipsThinkingLines(t *testing.T) {
	pattern := regexp.MustCompile("(?i)quota")
	text := "working\n∴ maybe the quota is exhausted\n● bash ls\n  ⎿ ok: a"
	if m, ok := availability.MatchLimit(text, []*regexp.Regexp{pattern}, testNow(), time.Hour); ok {
		t.Errorf("a thinking line matched a limit pattern: %q", m.Line)
	}

	hit := text + "\nerror: quota exceeded, resets in 2h"
	m, ok := availability.MatchLimit(hit, []*regexp.Regexp{pattern}, testNow(), time.Hour)
	if !ok {
		t.Fatalf("a real limit line was not matched")
	}
	if m.Line != "error: quota exceeded, resets in 2h" {
		t.Errorf("Line = %q, want the real limit line", m.Line)
	}
}

func TestMatchDenialSkipsThinkingLines(t *testing.T) {
	pattern := regexp.MustCompile("(?i)permission denied")
	text := "working\n∴ permission denied would be bad\n● bash ls\n  ⎿ ok: a"
	if line, ok := matchDenial(text, []*regexp.Regexp{pattern}); ok {
		t.Errorf("a thinking line matched a denial pattern: %q", line)
	}

	hit := text + "\nerror: permission denied by policy"
	line, ok := matchDenial(hit, []*regexp.Regexp{pattern})
	if !ok {
		t.Fatalf("a real denial line was not matched")
	}
	if line != "error: permission denied by policy" {
		t.Errorf("line = %q, want the real denial line", line)
	}
}
