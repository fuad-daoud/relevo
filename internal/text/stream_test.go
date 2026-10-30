package text_test

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/text"
)

// TestLastStreamKind pins the stream-kind choice: no segments keep the
// builder kind, one segment wins over it, and the last segment wins when a
// mid-round switch recorded several.
func TestLastStreamKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		builder  string
		segments []string
		want     string
	}{
		{name: "empty segments keep the builder kind", builder: "claude", segments: []string{}, want: "claude"},
		{name: "nil segments keep the builder kind", builder: "claude", segments: nil, want: "claude"},
		{name: "single segment wins", builder: "claude", segments: []string{"codex"}, want: "codex"},
		{name: "last segment wins after a switch", builder: "claude", segments: []string{"codex", "gemini"}, want: "gemini"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := text.LastStreamKind(c.builder, c.segments); got != c.want {
				t.Errorf("LastStreamKind(%q, %v) = %q, want %q", c.builder, c.segments, got, c.want)
			}
		})
	}
}
