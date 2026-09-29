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
