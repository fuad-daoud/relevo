package sanitize

import "testing"

// TestText pins the one sanitising rule every caller now shares: the cases are
// the TUI's own detail_test.go table, so a change here is a change the TUI
// tests would also catch.
func TestText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"vt", "a\x0bb", "a\uFFFDb"},
		{"ff", "a\x0cb", "a\uFFFDb"},
		{"crlf", "a\r\nb", "a\nb"},
		{"lone cr", "a\rb", "ab"},
		{"tab", "a\tb", "a    b"},
		{"esc sequence", "a\x1b[2Jb", "a\uFFFD[2Jb"},
		{"null and ack", "a\x00\x06b", "a\uFFFD\uFFFDb"},
		{"invalid utf8", "a\xffb", "a\uFFFDb"},
		{"c1 nel", "a\u0085b", "a\uFFFDb"},
		{"unicode unchanged", "héllo — ✓", "héllo — ✓"},
		{"newline unchanged", "line1\nline2", "line1\nline2"},
		{"empty", "", ""},
		{"plain text unchanged", "plain text", "plain text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.in); got != tc.want {
				t.Fatalf("Text(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
