package remote

import (
	"strings"
	"testing"
)

func TestIs40HexAcceptsOnlyAFullSha(t *testing.T) {
	cases := map[string]bool{
		strings.Repeat("a", 40):                    true,
		strings.Repeat("F", 40):                    true,
		"0123456789abcdefABCDEF0123456789abcdef01": true,
		strings.Repeat("a", 39):                    false,
		strings.Repeat("a", 41):                    false,
		strings.Repeat("a", 39) + "g":              false,
		strings.Repeat("a", 39) + "/":              false,
		strings.Repeat("a", 39) + ":":              false,
		strings.Repeat("a", 39) + "G":              false,
		strings.Repeat("a", 39) + "`":              false,
		"main":                                     false,
	}
	for s, want := range cases {
		if got := Is40Hex(s); got != want {
			t.Errorf("Is40Hex(%q) = %v, want %v", s, got, want)
		}
	}
}
