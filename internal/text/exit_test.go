package text_test

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/text"
)

// TestDenialSuffix pins the exit entry's permission-blocked suffix: empty
// when the tail held no denial, otherwise the denial line under a fixed
// marker, so a clean exit never reads as blocked.
func TestDenialSuffix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		line     string
		isDenial bool
		want     string
	}{
		{name: "no denial is empty", line: "", isDenial: false, want: ""},
		{name: "a line without the denial flag is empty", line: "Permission denied: read operation rejected", isDenial: false, want: ""},
		{name: "denial names the line", line: "Permission denied: read operation rejected", isDenial: true, want: "; permission-blocked: Permission denied: read operation rejected"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := text.DenialSuffix(tc.line, tc.isDenial); got != tc.want {
				t.Errorf("DenialSuffix(%q, %v) = %q, want %q", tc.line, tc.isDenial, got, tc.want)
			}
		})
	}
}
