package stats

import "testing"

func TestFitKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		s        string
		width    int
		clipLeft bool
		want     string
	}{
		{"short key pads", "abc", 5, false, "abc  "},
		{"long key clips right", "abcdefgh", 5, false, "abcd…"},
		{"long key clips left for a repo tail", "abcdefgh", 5, true, "…efgh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := FitKey(c.s, c.width, c.clipLeft); got != c.want {
				t.Errorf("FitKey(%q, %d, %v) = %q, want %q", c.s, c.width, c.clipLeft, got, c.want)
			}
		})
	}
}

// TestFormats pins the report's small text formatters.
func TestFormats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  func() string
		want string
	}{
		{"PctText with no closed round is -", func() string { return PctText(85.4, 0) }, "-"},
		{"PctText rounds to a whole percent", func() string { return PctText(85.4, 10) }, "85%"},
		{"TTFTText without a measurement is -", func() string { return TTFTText(ScoreRow{HasTTFT: false}) }, "-"},
		{"TTFTText renders seconds", func() string { return TTFTText(ScoreRow{HasTTFT: true, TTFTMS: 2500}) }, "2.5s"},
		{"Duration under an hour is minutes", func() string { return Duration(150_000) }, "2m"},
		{"Duration at an hour is hMM", func() string { return Duration(3_900_000) }, "1h05m"},
		{"MonthDay trims a full day", func() string { return MonthDay("2026-09-25") }, "09-25"},
		{"MonthDay leaves a short string", func() string { return MonthDay("short") }, "short"},
		{"ShortTokens is plain under a thousand", func() string { return ShortTokens(999) }, "999"},
		{"ShortTokens scales to k", func() string { return ShortTokens(1500) }, "1.5k"},
		{"ShortTokens strips a trailing .0", func() string { return ShortTokens(2_000_000) }, "2M"},
		{"ShortTokens scales to B", func() string { return ShortTokens(1_195_000_000) }, "1.2B"},
		{"ShortTokens leaves a whole billion bare", func() string { return ShortTokens(1_000_000_000) }, "1B"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.got(); got != c.want {
				t.Errorf("%s = %q, want %q", c.name, got, c.want)
			}
		})
	}
}
