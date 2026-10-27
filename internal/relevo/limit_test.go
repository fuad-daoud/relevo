package relevo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
func gateHeadless(t *testing.T, rt Runtime, b store.Binding, text string, resetMS int64, closeOld bool) (store.Binding, availability.LimitMatch, bool, error) {
	t.Helper()
	var next store.Binding
	var m availability.LimitMatch
	var handled bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, m, handled, err = gateOnLimit(context.Background(), rt, tx, b, text, resetMS, closeOld)
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

// museGateNow is the incident's own minute: the failed run ended 11:03:32Z
// and the switch gated 11:03:33Z, both before the 13:22:58Z reset the stream
// beside the text already names.
var museGateNow = time.Date(2026, 10, 24, 11, 3, 33, 0, time.UTC)

// museGateSetup binds webshop headless on a muse candidate and writes stream,
// the raw muse stream the scan reads: one event per line.
func museGateSetup(t *testing.T, stream string) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = newFakeRunner()
	rt.Candidates = candidateSet(t, `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"]}]`)
	rt.Policy = orderOf("builder", "muse/meta/m")
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "muse/meta/m", MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.Kind = "muse"
	b.Builder.StreamRound = b.Round
	b.Builder.StreamStart = 0
	streamPath := rt.Store.StreamPath(b.Name, b.Round)
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(streamPath, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	rt.Now = func() time.Time { return museGateNow }
	return rt, b
}

// museGateUsageLine renders one muse run.usage event carrying an allowance
// window reset, the structured field a stream carries beside any failure.
func museGateUsageLine(windowMS int64) string {
	return `{"payload_type":"run.usage","payload":{"allowance":{"window":{"resets_at_ms":` +
		strconv.FormatInt(windowMS, 10) + `,"used_percent":4}},"models":[]}}`
}

// museGateFailedLine renders one muse run.terminal.failed event with a plain
// quota message and no reset sentence, optionally carrying the failure's own
// quota instant.
func museGateFailedLine(quotaMS int64) string {
	quota := "null"
	if quotaMS != 0 {
		quota = `{"limit_kind":"tierLimit","resets_at_ms":` + strconv.FormatInt(quotaMS, 10) + `}`
	}
	return `{"payload_type":"run.terminal.failed","payload":{"terminal":"failed","error":{"message":"Usage limit reached for your plan","quota":` + quota + `}}}`
}

// TestGateOnLimitMuseStructuredStream pins the whole thread: an
// allowance-carrying muse stream gates at its structured instant even though
// the matched text line names no reset, and the failure's own quota instant
// wins over the window's. The scan reads the stream file the round already
// wrote; no other file is read.
func TestGateOnLimitMuseStructuredStream(t *testing.T) {
	t.Parallel()

	const quotaMS = 1792848178000
	const windowMS = 1792851778000
	quotaReset := time.Date(2026, 10, 24, 13, 22, 58, 0, time.UTC)
	windowReset := time.Date(2026, 10, 24, 14, 22, 58, 0, time.UTC)

	for _, c := range []struct {
		name   string
		stream string
		want   time.Time
	}{
		{
			name:   "allowance window gates with no text reset",
			stream: museGateUsageLine(windowMS) + "\n" + museGateFailedLine(0) + "\n",
			want:   windowReset,
		},
		{
			name:   "failed quota beats the allowance window",
			stream: museGateUsageLine(windowMS) + "\n" + museGateFailedLine(quotaMS) + "\n",
			want:   quotaReset,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			rt, b := museGateSetup(t, c.stream)

			text, resetMS := limitScan(context.Background(), rt, b)
			if !strings.Contains(text, "Usage limit reached for your plan") {
				t.Errorf("scan text = %q, want the failure message", text)
			}
			if strings.Contains(text, "resets_at_ms") {
				t.Errorf("scan text = %q, want rendered text without raw JSON", text)
			}

			reportPath := rt.Store.ReportPath("webshop", 1)
			if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(reportPath, []byte("done"), 0o644); err != nil {
				t.Fatal(err)
			}

			_, m, handled, err := gateHeadless(t, rt, b, text, resetMS, false)
			if err != nil {
				t.Fatalf("gateOnLimit: %v", err)
			}
			if handled {
				t.Error("handled = true, want false with a report on disk")
			}
			if !m.Parsed {
				t.Errorf("Parsed = false, want true (Until %v is the fallback)", m.Until)
			}
			if !m.Until.Equal(c.want) {
				t.Errorf("m.Until = %v, want the structured instant %v", m.Until, c.want)
			}
			rl := rateLimitedEntries(loadLedger(t, rt))
			if len(rl) != 1 {
				t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
			}
			e := rl[0]
			if !e.Until.Equal(c.want) {
				t.Errorf("entry Until = %v, want %v", e.Until, c.want)
			}
			if e.Subject != "meta" || e.Source != "relevo" || e.Binding != "webshop" {
				t.Errorf("entry = %+v, want Subject=meta Source=relevo Binding=webshop", e)
			}
			if e.Note != "Usage limit reached for your plan" {
				t.Errorf("entry Note = %q, want the failure message", e.Note)
			}
		})
	}
}

func TestGateOnLimit(t *testing.T) {
	t.Parallel()

	t.Run("match, no report", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		rt = at(rt, time.Minute)

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, 0, false)
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

		got, m, handled, err := gateHeadless(t, rt, b, "boom: out of tokens", 0, false)
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

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, 0, false)
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

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, 0, false)
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

	t.Run("structured reset outranks the text", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		rt.Now = func() time.Time { return museGateNow }

		// The text names a prose reset; the structured instant beside it is
		// earlier and must win.
		text := "Individual quota reached. Resets in 2h48m52s."
		want := museGateNow.Add(90 * time.Minute)
		got, m, handled, err := gateHeadless(t, rt, b, text, want.UnixMilli(), false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if !handled {
			t.Fatal("handled = false, want true")
		}
		if !m.Parsed {
			t.Error("Parsed = false, want true")
		}
		if !m.Until.Equal(want) {
			t.Errorf("m.Until = %v, want the structured instant %v", m.Until, want)
		}
		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
		}
		if !rl[0].Until.Equal(want) {
			t.Errorf("entry Until = %v, want %v", rl[0].Until, want)
		}
		if got.BuilderCandidate != testClaudeRef {
			t.Errorf("BuilderCandidate = %q, want the switch to %q", got.BuilderCandidate, testClaudeRef)
		}
	})

	t.Run("ledger write failure", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		// A KV whose ledger put fails: the record cannot be written, so the
		// switch must proceed anyway.
		rt.Gates = failPutKV{inner: rt.Gates, key: "ledger"}

		_, _, handled, err := gateHeadless(t, rt, b, gateFixtureLine, 0, false)
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
