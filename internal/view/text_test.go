package view

import "testing"

// TestHumanBytesRendersBinaryUnits pins the shared size formatter: bytes below
// a kibibyte stay whole, and larger sizes carry one decimal and a binary unit.
func TestHumanBytesRendersBinaryUnits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		n    int64
		want string
	}{
		{"zero bytes", 0, "0 B"},
		{"bytes below a kibibyte", 512, "512 B"},
		{"exactly a kibibyte", 1024, "1.0 KiB"},
		{"kibibytes with a fraction", 1536, "1.5 KiB"},
		{"exactly a mebibyte", 1 << 20, "1.0 MiB"},
		{"mebibytes with a fraction", 3*1024*1024 + 512*1024, "3.5 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := HumanBytes(tc.n); got != tc.want {
				t.Errorf("HumanBytes(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}
