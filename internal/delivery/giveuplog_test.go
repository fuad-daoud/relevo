package delivery

import (
	"testing"
	"time"
)

func TestGiveUpLog(t *testing.T) {
	t.Parallel()

	var g giveUpLog
	t0 := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	if !g.shouldLog("a", t0) {
		t.Errorf("shouldLog(a, t0) = false, want true")
	}
	if g.shouldLog("a", t0.Add(time.Second)) {
		t.Errorf("shouldLog(a, t0+1s) = true, want false")
	}
	if g.shouldLog("a", t0.Add(59*time.Minute)) {
		t.Errorf("shouldLog(a, t0+59m) = true, want false")
	}

	if !g.shouldLog("b", t0.Add(time.Second)) {
		t.Errorf("shouldLog(b, t0+1s) = false, want true")
	}

	if !g.shouldLog("a", t0.Add(60*time.Minute)) {
		t.Errorf("shouldLog(a, t0+60m) = false, want true")
	}

	if !g.shouldLog("c", t0.Add(3*time.Hour)) {
		t.Errorf("shouldLog(c, t0+3h) = false, want true")
	}
	if len(g.last) != 1 {
		t.Fatalf("len(g.last) = %d, want 1 (contents: %+v)", len(g.last), g.last)
	}
	if _, ok := g.last["c"]; !ok {
		t.Errorf("g.last does not contain c: %+v", g.last)
	}
}
