package availability

import (
	"testing"
	"time"
)

func TestLoadLatencyMissingIsEmpty(t *testing.T) {
	t.Parallel()
	h, err := LoadLatency(testDeps(t))
	if err != nil {
		t.Fatalf("LoadLatency(missing) error = %v, want nil", err)
	}
	if len(h.Samples) != 0 {
		t.Errorf("LoadLatency(missing).Samples = %+v, want empty", h.Samples)
	}
}

// A sample written as a row reads back as the sample it was: the read is the
// shape every display takes, so the conversion is the whole claim here.
func TestLatencyRowReadsBackAsItsSample(t *testing.T) {
	t.Parallel()
	d := testDeps(t)
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	if err := insertSampleRow(d, Sample{
		At: at, Token: "claude/test/m", Host: "box", TTFTMS: 640, TotalMS: 900,
	}); err != nil {
		t.Fatalf("insertSampleRow: %v", err)
	}

	got, err := LoadLatency(d)
	if err != nil {
		t.Fatalf("LoadLatency() error = %v", err)
	}
	if len(got.Samples) != 1 {
		t.Fatalf("LoadLatency() = %+v, want 1 sample", got.Samples)
	}
	s := got.Samples[0]
	if s.Token != "claude/test/m" || s.Host != "box" || s.TTFTMS != 640 || s.TotalMS != 900 || s.Err != "" {
		t.Errorf("read back = %+v, want the written sample", s)
	}
	if !s.At.Equal(at) {
		t.Errorf("At = %v, want %v", s.At, at)
	}
}

func TestPruneDropsOlderThan30Days(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	h := LatencyHistory{Samples: []Sample{
		{At: now.Add(-31 * 24 * time.Hour), Token: "old"},
		{At: now.Add(-29 * 24 * time.Hour), Token: "new"},
	}}

	got := h.Prune(now)
	if len(got.Samples) != 1 || got.Samples[0].Token != "new" {
		t.Errorf("Prune() = %+v, want only the 29-day sample", got.Samples)
	}
	if len(h.Samples) != 2 {
		t.Errorf("Prune mutated the receiver: %+v", h.Samples)
	}
}

func TestSummaryLowerMedianIgnoresErrors(t *testing.T) {
	const token = "claude/test/m"
	h := LatencyHistory{Samples: []Sample{
		{Token: token, TTFTMS: 900},
		{Token: token, TTFTMS: 600},
		{Token: token, TTFTMS: 700},
		{Token: token, TTFTMS: 100, Err: "boom"},
	}}

	got := h.Summary(token)
	if got.N != 3 || got.Errors != 1 || got.TTFTP50MS != 700 {
		t.Errorf("Summary() = %+v, want {N:3 Errors:1 TTFTP50MS:700}", got)
	}
}

func TestSummaryOtherTokenIgnored(t *testing.T) {
	const token = "claude/test/m"
	h := LatencyHistory{Samples: []Sample{
		{Token: token, TTFTMS: 100},
		{Token: "opencode/other/m", TTFTMS: 5000},
	}}

	got := h.Summary(token)
	if got.N != 1 || got.Errors != 0 || got.TTFTP50MS != 100 {
		t.Errorf("Summary() = %+v, want only claude/test/m's sample", got)
	}
}
