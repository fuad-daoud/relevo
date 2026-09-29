package usage

import (
	"os"
	"testing"
)

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// mustTemp writes body to a temp file and returns it open at offset 0, plus its
// path.
func mustTemp(t *testing.T, body string) (*os.File, string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, f.Name()
}

func testPrices() Prices {
	return Prices{AsOf: "2026-09-18", Models: map[string]ModelPrice{
		// USD per million tokens, round numbers so the sums are exact.
		"anthropic/claude-sonnet-5": {In: 3, CacheRead: 0.3, CacheWrite: 3.75, Out: 15},
		"google/gemini-3.8-flash":   {In: 0.5, CacheRead: 0.05, CacheWrite: 0, Out: 2},
	}}
}
