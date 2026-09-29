package ui

import "testing"

func TestShortVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v0.13.0-28-gb66c6fc", "v0.13.0-28"},
		{"v0.13.0-28-gb66c6fc-dirty", "v0.13.0-28"},
		{"v0.13.0", "v0.13.0"},
		{"", ""},
		{"custom-build", "custom-build"},
	}
	for _, tc := range cases {
		if got := shortVersion(tc.in); got != tc.want {
			t.Errorf("shortVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
