package relevo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

func testNow() time.Time {
	return time.Date(2026, 9, 13, 23, 13, 0, 0, time.FixedZone("EEST", 3*3600))
}

// gateOnLimitSetup is TestReconcileHeadlessGatedKillsAndSwitches's own setup
// (two-provider set, headless bind on agy/other/m, the three-builder order,
// one Send), without the Unavailable call gateOnLimit is meant to replace.
// gateHeadless runs gateOnLimit on b inside the lock, the way switchHeadless
// runs switchBuilder.
func gateHeadless(t *testing.T, rt Runtime, b store.Binding, text string, closeOld bool) (store.Binding, availability.LimitMatch, bool, error) {
	t.Helper()
	var next store.Binding
	var m availability.LimitMatch
	var handled bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, m, handled, err = gateOnLimit(context.Background(), rt, tx, b, text, closeOld)
		return err
	})
	return next, m, handled, err
}

func rateLimitedEntries(l availability.Ledger) []availability.Entry {
	var out []availability.Entry
	for _, e := range l.Entries {
		if e.Kind == availability.RateLimited {
			out = append(out, e)
		}
	}
	return out
}

const gateFixtureLine = "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s."

func TestGateOnLimit(t *testing.T) {
	t.Parallel()

	t.Run("match, no report", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		rt = at(rt, time.Minute)

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if !handled {
			t.Fatal("handled = false, want true")
		}

		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
		}
		e := rl[0]
		if e.Subject != "other" || e.Source != "relevo" || e.Binding != "webshop" || e.Note != gateFixtureLine {
			t.Errorf("entry = %+v, want Subject=other Source=relevo Binding=webshop Note=%q", e, gateFixtureLine)
		}
		wantUntil := rt.Now().Add(2*time.Hour + 48*time.Minute + 52*time.Second).UTC()
		if !e.Until.Equal(wantUntil) {
			t.Errorf("Until = %v, want %v", e.Until, wantUntil)
		}
		if !m.Until.Equal(wantUntil) || !m.Parsed {
			t.Errorf("m = %+v, want Until=%v Parsed=true", m, wantUntil)
		}

		sw := switches(t, rt)
		if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (rate-limited: Individual quota reached") {
			t.Errorf("switch entries = %+v", sw)
		}
		if got.RoundSwitches != 0 {
			t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
		}
		if got.BuilderCandidate != testClaudeRef {
			t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
		}
	})

	t.Run("no match", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)

		got, m, handled, err := gateHeadless(t, rt, b, "boom: out of tokens", false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if handled {
			t.Error("handled = true, want false")
		}
		if m.Line != "" {
			t.Errorf("m.Line = %q, want empty", m.Line)
		}
		if l := loadLedger(t, rt); len(l.Entries) != 0 {
			t.Errorf("ledger entries = %+v, want none", l.Entries)
		}
		if sw := switches(t, rt); len(sw) != 0 {
			t.Errorf("switch entries = %+v, want none", sw)
		}
		if !reflect.DeepEqual(got, b) {
			t.Errorf("got = %+v, want unchanged b %+v", got, b)
		}
	})

	t.Run("match with a report on disk", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		rt = at(rt, time.Minute)
		reportPath := rt.Store.ReportPath("webshop", 1)
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reportPath, []byte("done"), 0o644); err != nil {
			t.Fatal(err)
		}
		specsBefore := len(fr.specs)

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if handled {
			t.Error("handled = true, want false")
		}
		if m.Line != gateFixtureLine {
			t.Errorf("m.Line = %q, want %q", m.Line, gateFixtureLine)
		}
		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1", len(rl))
		}
		if sw := switches(t, rt); len(sw) != 0 {
			t.Errorf("switch entries = %+v, want none", sw)
		}
		if len(fr.specs) != specsBefore {
			t.Errorf("fr.specs grew from %d to %d, want no new process", specsBefore, len(fr.specs))
		}
		if got.BuilderCandidate != b.BuilderCandidate {
			t.Errorf("BuilderCandidate changed to %q, want unchanged %q", got.BuilderCandidate, b.BuilderCandidate)
		}
	})

	t.Run("not switchable", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		b.BuilderCandidate = ""

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if handled {
			t.Error("handled = true, want false")
		}
		if m.Line != "" {
			t.Errorf("m.Line = %q, want empty", m.Line)
		}
		if l := loadLedger(t, rt); len(l.Entries) != 0 {
			t.Errorf("ledger entries = %+v, want none", l.Entries)
		}
		if !reflect.DeepEqual(got, b) {
			t.Errorf("got = %+v, want unchanged b %+v", got, b)
		}
	})

	t.Run("ledger write failure", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		// A KV whose ledger put fails: the record cannot be written, so the
		// switch must proceed anyway.
		rt.Gates = failPutKV{inner: rt.Gates, key: "ledger"}

		_, _, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if !handled {
			t.Error("handled = false, want true -- a ledger write failure must not block the switch")
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatalf("ReadLog: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Kind == store.KindSwitch {
				found = true
			}
		}
		if !found {
			t.Error("no switch log entry, want the switch to have happened despite the ledger write failure")
		}
	})
}
