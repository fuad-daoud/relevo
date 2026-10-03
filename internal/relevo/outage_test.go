package relevo

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// A provider-side outage behind a no-report exit gates TIMED, like a rate
// limit, with the cause kept in the gate note. Only a genuine crash with no
// provider text stays until-cleared through the round-exclusion path.
//
// These three tests are the whole contract. A redrains the outage case, B pins
// the crash case that must not move, and C is the anti-regression guard: every
// shape of the 5xx/UNAVAILABLE class must land on the timed gate, so a future
// mapping of that class back onto ExitedNoReport fails a named test.

// outageFixtureLine is agy-errors row 7, the observation behind this round: an agy
// ERROR result whose error field is a provider with no capacity. It is a real
// transcript fixture, not a string invented for the test.
const outageFixtureLine = `API error (attempt 1): UNAVAILABLE (code 503): No capacity available for model gemini-3.8-flash-high on the server`

// crashFixtureLine is the control: a builder that died. It names no provider
// status, no 5xx and no reset, and it matches no limit or denial pattern, so
// nothing but the crash path can claim it.
const crashFixtureLine = `panic: runtime error: invalid memory address or nil pointer dereference`

// outageExit stages the exited-without-report headless tick: the round's stream
// carries raw (real harness lines), the stream cursor points at the start of
// this process's own bytes so limitText reads exactly them, and the process is
// scripted dead with code.
func outageExit(t *testing.T, rt Runtime, b store.Binding, raw string, code int) store.Binding {
	t.Helper()
	streamWrite(t, rt, raw)
	b.Builder.StreamRound = b.Round
	b.Builder.StreamStart = 0
	fr := runnerOf(t, rt)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, code)
	return b
}

// assertTailIsNotALimit guards a test against passing for the wrong reason: it
// fails when the staged tail matches a LimitPattern, because gateOnLimit would
// then have claimed the exit before any outage classification ran.
func assertTailIsNotALimit(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	text := limitText(context.Background(), rt, b)
	if text == "" {
		t.Fatal("the staged tail scans as empty text: this test would pass with the scan removed")
	}
	patterns := availability.LimitPatterns(AvailabilityDeps(rt), b.BuilderCandidate)
	if m, ok := availability.MatchLimit(text, patterns, rt.Now(), rt.Policy.LimitGateDefault()); ok {
		t.Fatalf("the staged tail is a rate-limit line (%q): gateOnLimit would claim this exit before any outage classification, so the test proves nothing", m.Line)
	}
}

// TestReconcileHeadlessProviderOutageNoReportGatesTimed is case A: a
// provider that answers UNAVAILABLE (code 503) and then exits with no report
// is a provider gate, not a builder failure. The exit must record one timed
// rate_limited entry carrying the cause, switch the builder UNCUNTED, and leave
// RoundExcluded empty -- so the same candidate is eligible again once the
// provider recovers, instead of being locked out of the round.
func TestReconcileHeadlessProviderOutageNoReportGatesTimed(t *testing.T) {
	t.Parallel()

	t.Run("bare provider subject", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = outageExit(t, rt, b, jsonlLine(t, "agy-errors/results.jsonl", 6), 1)
		assertTailIsNotALimit(t, rt, b)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}

		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
		}
		e := rl[0]
		if e.Subject != "other" || e.Source != "relevo" || e.Binding != "webshop" {
			t.Errorf("entry = %+v, want Subject=other Source=relevo Binding=webshop", e)
		}
		if !strings.Contains(e.Note, "UNAVAILABLE") || !strings.Contains(e.Note, "code 503") {
			t.Errorf("Note = %q, want it to keep the outage cause (%q)", e.Note, outageFixtureLine)
		}
		wantUntil := rt.Now().Add(rt.Policy.LimitGateDefault()).UTC()
		if e.Until.IsZero() {
			t.Fatalf("Until is zero: a provider outage must gate timed, not until cleared")
		}
		if !e.Until.Equal(wantUntil) {
			t.Errorf("Until = %v, want %v (the line names no reset, so the fallback applies)", e.Until, wantUntil)
		}

		if got.RoundSwitches != 0 {
			t.Errorf("RoundSwitches = %d, want 0: a provider closing is not a builder failing", got.RoundSwitches)
		}
		if len(got.RoundExcluded) != 0 {
			t.Errorf("RoundExcluded = %v, want empty: an outage is a provider gate, never the #191 exclusion", got.RoundExcluded)
		}

		sw := switches(t, rt)
		if len(sw) != 1 {
			t.Fatalf("switch entries = %+v, want exactly one", sw)
		}
		if !strings.Contains(sw[0].Note, "503") {
			t.Errorf("switch note = %q, want it to keep the outage cause", sw[0].Note)
		}
		if got.BuilderCandidate != testClaudeRef {
			t.Errorf("BuilderCandidate = %q, want %q: the gated provider is skipped, the candidate is not excluded", got.BuilderCandidate, testClaudeRef)
		}
		if got.Round != 1 {
			t.Errorf("Round = %d, want 1: the replacement starts on the same round", got.Round)
		}
	})

	t.Run("group@account subject", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := accountRotateSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = outageExit(t, rt, b, "opencode: upstream returned 503 Service Unavailable, the provider has no capacity\n", 1)
		assertTailIsNotALimit(t, rt, b)

		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}

		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
		}
		// The gate lands on the login that hit it, so another login of the same
		// provider stays usable -- the same rule gateOnLimit follows.
		if want := "test@cp2"; rl[0].Subject != want {
			t.Errorf("Subject = %q, want %q", rl[0].Subject, want)
		}
		if rl[0].Until.IsZero() {
			t.Error("Until is zero, want a timed gate")
		}
	})

	t.Run("ledger write failure still switches", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = outageExit(t, rt, b, jsonlLine(t, "agy-errors/results.jsonl", 6), 1)
		// A KV whose ledger put fails: the record cannot be written, so the
		// switch must proceed anyway -- the same contract gateOnLimit keeps.
		rt.Gates = failPutKV{inner: rt.Gates, key: "ledger"}

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if sw := switches(t, rt); len(sw) != 1 {
			t.Errorf("switch entries = %+v, want exactly one despite the ledger write failure", sw)
		}
		if len(got.RoundExcluded) != 0 {
			t.Errorf("RoundExcluded = %v, want empty", got.RoundExcluded)
		}
	})
}

// TestReconcileHeadlessCrashWithoutOutageStaysUntilCleared is case B: the pin.
// A builder that simply died, with nothing in its tail naming a provider
// fault, keeps every part of the exclusion behaviour -- RoundExcluded grows, the
// switch counts, and nothing is written to the ledger. It guards against the
// over-correction: a classifier loose enough to claim ordinary crashes would
// leave a genuinely broken candidate eligible again on the same provider.
func TestReconcileHeadlessCrashWithoutOutageStaysUntilCleared(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = outageExit(t, rt, b, crashFixtureLine+"\n", 3)
	assertTailIsNotALimit(t, rt, b)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != "agy/other/m" {
		t.Errorf("RoundExcluded = %v, want [agy/other/m]: a genuine crash stays excluded for the rest of the round", got.RoundExcluded)
	}
	if got.RoundSwitches != 1 {
		t.Errorf("RoundSwitches = %d, want 1 (counted): a crashing builder spends the round's switch budget", got.RoundSwitches)
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
		t.Errorf("rate_limited entries = %+v, want none: a crash gates no provider", rl)
	}
	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Fatalf("switch entries = %+v, want exactly one", sw)
	}
	if !strings.Contains(sw[0].Note, "exited (code 3)") {
		t.Errorf("switch note = %q, want the until-cleared exclusion reason", sw[0].Note)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
}

// TestProviderOutageClassNeverGatesUntilCleared is case C: the anti-regression
// guard. Every shape of the 5xx/UNAVAILABLE class the classifier is meant to
// know must land on the timed gate with no exclusion. A future change that maps
// any of these back onto the until-cleared ExitedNoReport exclusion turns this
// red, by name.
func TestProviderOutageClassNeverGatesUntilCleared(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"gRPC UNAVAILABLE on an agy ERROR result", jsonlLine(t, "agy-errors/results.jsonl", 6)},
		{"UNAVAILABLE alone", "API error: UNAVAILABLE\n"},
		{"code 503", "API error (attempt 4): code 503 from the upstream\n"},
		{"HTTP status line", "POST /v1/messages -> HTTP 502 Bad Gateway\n"},
		// agy's real ERROR-result shape. Only result.error reaches a scan
		// (transcript.LimitLines), so the status has to be inside the error
		// TEXT -- which is why the classifier keys on the message, never on a
		// structured code field.
		{"agy ERROR result, lowercase message", `{"event":"result","result":{"conversation_id":"c8","status":"ERROR","response":"","error":"upstream unavailable (code 503)"}}` + "\n"},
		{"status code in the message", "request failed: the upstream returned status code 502\n"},
		{"provider's own words", "the model endpoint reported internal server error and stopped\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := newFakeRunner()
			rt, b := gateOnLimitSetup(t, fr)
			rt = at(rt, 10*time.Minute)
			b = outageExit(t, rt, b, c.raw, 1)
			assertTailIsNotALimit(t, rt, b)

			got, err := reconcile(t, rt, b)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			rl := rateLimitedEntries(loadLedger(t, rt))
			if len(rl) != 1 {
				t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
			}
			if rl[0].Until.IsZero() {
				t.Error("Until is zero: this outage class must gate timed, never until cleared")
			}
			if rl[0].Note == "" {
				t.Error("Note is empty: the cause must be kept in the gate note")
			}
			if len(got.RoundExcluded) != 0 {
				t.Errorf("RoundExcluded = %v, want empty: the outage class must never reach the #191 exclusion", got.RoundExcluded)
			}
			if got.RoundSwitches != 0 {
				t.Errorf("RoundSwitches = %d, want 0 (uncounted)", got.RoundSwitches)
			}
		})
	}
}

// TestProviderOutageNoMatchLeavesExclusionUntouched is the guard's other edge,
// in the same file on purpose: a tail the classifier does not know must leave
// the crash path byte-for-byte as it was. It is what makes the table above
// mean something -- without it, "everything times" would pass C too.
func TestProviderOutageNoMatchLeavesExclusionUntouched(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		crashFixtureLine + "\n",
		"",
		"fatal error: all goroutines are asleep - deadlock!\n",
		"2026/09/14 10:04:11 worker exited after 503 processed jobs\n", // 503 is a count, not a status
		"read tcp 10.0.0.1:5000->10.0.0.2:443: connection reset by peer\n",
		// agy-errors row 6: a real connection failure, not a provider outage.
		jsonlLine(t, "agy-errors/results.jsonl", 5),
		// The one shape the classifier cannot see, pinned so a future change
		// to transcript.LimitLines that starts passing it is a deliberate act:
		// a STRUCTURED code field never reaches a scan -- LimitLines extracts
		// error.message -- so a 503 in the object beside the message is
		// invisible here and the exit stays until-cleared.
		`{"event":"result","result":{"conversation_id":"c9","status":"ERROR","response":"","error":{"code":503,"message":"the provider has no capacity"}}}` + "\n",
	} {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = outageExit(t, rt, b, raw, 3)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile (tail %q): %v", raw, err)
		}
		if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != "agy/other/m" {
			t.Errorf("tail %q: RoundExcluded = %v, want [agy/other/m]: an unknown tail must stay until-cleared", raw, got.RoundExcluded)
		}
		if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
			t.Errorf("tail %q: rate_limited entries = %+v, want none", raw, rl)
		}
		if got.RoundSwitches != 1 {
			t.Errorf("tail %q: RoundSwitches = %d, want 1 (counted)", raw, got.RoundSwitches)
		}
	}
}

// TestProviderOutageClassificationIsNotAReportPresentPath pins the last case in
// the plan: the outage classification never fires when a report or marker close
// applies. With the report on disk the round closes through the unmarked-exit
// path, which is untouched.
func TestProviderOutageClassificationIsNotAReportPresentPath(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = outageExit(t, rt, b, jsonlLine(t, "agy-errors/results.jsonl", 6), 1)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %+v, want none: a report on disk closes the round, nothing is gated or switched", sw)
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
		t.Errorf("rate_limited entries = %+v, want none: the existing gateOnLimit report-on-disk branch is untouched", rl)
	}
}
