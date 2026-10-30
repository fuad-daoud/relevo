package sanitize

import "testing"

func TestStripControls(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text passes", "hello, world", "hello, world"},
		{"non-ASCII passes", "héllo — 日本", "héllo — 日本"},
		{"C0 controls go, newline and tab too", "a\x00b\nc\td\re", "abcde"},
		{"an escape sequence loses its ESC", "\x1b[31mred", "[31mred"},
		{"DEL goes", "a\x7fb", "ab"},
		{"C1 controls go, both edges", "a\u0080b\u0085c\u009fd", "abcd"},
		{"the rune after C1 stays", "a b", "a b"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripControls(tt.in); got != tt.want {
				t.Fatalf("StripControls(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
