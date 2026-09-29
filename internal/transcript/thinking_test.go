package transcript

import (
	"reflect"
	"testing"
)

func TestThinkingLinesMarksEveryLine(t *testing.T) {
	got := thinkingLines("\n  \nfirst\n\nsecond\r\n\n")
	want := []string{"∴ first", "∴", "∴ second"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("thinkingLines = %q, want %q", got, want)
	}
	for _, in := range []string{"", "  \n "} {
		if got := thinkingLines(in); got != nil {
			t.Errorf("thinkingLines(%q) = %q, want nil", in, got)
		}
	}
}

func TestIsThinking(t *testing.T) {
	for _, line := range []string{"∴ x", "∴"} {
		if !IsThinking(line) {
			t.Errorf("IsThinking(%q) = false, want true", line)
		}
	}
	for _, line := range []string{"x ∴", "● bash ls", ""} {
		if IsThinking(line) {
			t.Errorf("IsThinking(%q) = true, want false", line)
		}
	}
}

// TestIsThinkingAcrossTheStamp: the marker is tested on the body after the
// stamp, so a stamped musing is still skipped by both scans.
func TestIsThinkingAcrossTheStamp(t *testing.T) {
	for _, line := range []string{"12:41:03 ∴ x", "+0.9s ∴ x", "12:41:03 +4.2s ∴ x"} {
		if !IsThinking(line) {
			t.Errorf("IsThinking(%q) = false, want true", line)
		}
	}
	for _, line := range []string{"12:41:03 ● bash ls", "12:41:03 +4.2s   ⎿ ok"} {
		if IsThinking(line) {
			t.Errorf("IsThinking(%q) = true, want false", line)
		}
	}
}
