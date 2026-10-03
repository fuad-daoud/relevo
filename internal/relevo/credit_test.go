package relevo

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// A builder that ran out of credits behind a no-report exit gates UNTIL
// CLEARED, unlike every other rate limit. The top-up that clears it is a human
// action with no time attached, so a timed gate here would fall open on its own
// and land fallbacks back on the same dead candidate once the timer ran out --
// the exact failure the class exists to prevent.
//
// These tests are the whole contract. A redtains the credit case, B pins the
// crash case that must not move, and C is the anti-regression guard: every
// shape of the credit class must land on the until-cleared gate, so a future
// mapping of that class back onto a timed Until fails a named test.

// creditFixtureLine is opencode-errors row 2, the observation behind this round:
// an opencode provider.quota error whose message says the account cannot afford
// the request. It is a real transcript fixture, not a string invented for the
// test, and the setup below binds opencode so the fixture's own channel renders
// it.
const creditFixtureLine = `{"error":{"timestamp":1790451684929,"sessionID":"ses_b","error":{"type":"provider.quota","message":"This request requires more credits, or fewer max_tokens. You requested up to 131072 tokens, but can only afford 34217.","status":402}}}`

// crashFixtureLine is the control, shared with outage_test.go: a builder that
// died, naming no balance, no provider status, no 5xx and no reset, so nothing
// but the crash path can claim it.

// creditSetup is gateOnLimitSetup's shape with the binding on opencode, so the
// real opencode credit fixture reaches a scan the way it reaches production: the
// provider the credit belongs to, and a claude fallback that stays usable
// because the gate lands on this provider alone.
func creditSetup(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", testOpencodeRef, testClaudeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
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
	return rt, b
}

// creditExit stages the exited-without-report headless tick: the round's stream
// carries the raw harness line, the stream cursor points at the start of this
// process's own bytes so limitText reads exactly them, and the process is
// scripted dead with code.
func creditExit(t *testing.T, rt Runtime, b store.Binding, raw string, code int) store.Binding {
	t.Helper()
	streamWrite(t, rt, raw)
	b.Builder.StreamRound = b.Round
	b.Builder.StreamStart = 0
	fr := runnerOf(t, rt)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, code)
	return b
}

// assertTailReachesScan guards a test against passing for the wrong reason: it
// fails when the staged tail scans as empty text, because every assertion below
// would then hold with the scan itself removed.
func assertTailReachesScan(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	if text := limitText(context.Background(), rt, b); text == "" {
		t.Fatal("the staged tail scans as empty text: this test would pass with the scan removed")
	}
}

// TestReconcileHeadlessCreditExhaustedNoReportGatesUntilCleared is case A: a
// builder whose tail says the account is out of credits is a credit gate, not a
// resetting rate limit and not a builder failure. The exit must record exactly
// one rate_limited entry with a ZERO Until, switch the builder UNCUNTED, and
// leave RoundExcluded empty -- so the candidate is only eligible again once a
// human clears the gate, instead of becoming eligible again on a timer.
func TestReconcileHeadlessCreditExhaustedNoReportGatesUntilCleared(t *testing.T) {
	t.Parallel()

	t.Run("bare provider subject", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := creditSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = creditExit(t, rt, b, jsonlLine(t, "opencode-errors/results.jsonl", 1), 1)
		assertTailReachesScan(t, rt, b)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}

		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
		}
		e := rl[0]
		if e.Subject != "test" || e.Source != "relevo" || e.Binding != "webshop" {
			t.Errorf("entry = %+v, want Subject=test Source=relevo Binding=webshop", e)
		}
		if !e.At.Equal(rt.Now().UTC()) {
			t.Errorf("At = %v, want %v", e.At, rt.Now().UTC())
		}
		if !e.Until.IsZero() {
			t.Errorf("Until = %v, want the zero value: a credit gate is until cleared, never timed", e.Until)
		}
		if !strings.Contains(e.Note, "requires more credits") {
			t.Errorf("Note = %q, want the matched credit line", e.Note)
		}
		if !strings.Contains(strings.ToLower(e.Note), "credit") {
			t.Errorf("Note = %q, want the top-up wording kept beside the line", e.Note)
		}

		if got.RoundSwitches != 0 {
			t.Errorf("RoundSwitches = %d, want 0: an empty balance is not a builder failing", got.RoundSwitches)
		}
		if len(got.RoundExcluded) != 0 {
			t.Errorf("RoundExcluded = %v, want empty: the until-cleared ledger gate is the exclusion, not #191", got.RoundExcluded)
		}
		if got.Round != 1 {
			t.Errorf("Round = %d, want 1: the replacement starts on the same round", got.Round)
		}

		sw := switches(t, rt)
		if len(sw) != 1 {
			t.Fatalf("switch entries = %+v, want exactly one", sw)
		}
		if !strings.Contains(strings.ToLower(sw[0].Note), "credit") {
			t.Errorf("switch note = %q, want it to keep the credit cause", sw[0].Note)
		}
		// The gate lands on the provider, and claude shares that provider, so
		// the replacement is agy -- the one builder on a provider the empty
		// balance says nothing about. This is what makes an until-cleared gate
		// usable: it skips exactly the provider that ran out.
		if got.BuilderCandidate != "agy/other/m" {
			t.Errorf("BuilderCandidate = %q, want agy/other/m: the gated provider is skipped, the candidate is not excluded", got.BuilderCandidate)
		}
	})

	t.Run("group@account subject", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := accountRotateSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = creditExit(t, rt, b, "opencode: this request requires more credits, top up the account\n", 1)
		assertTailReachesScan(t, rt, b)

		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}

		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
		}
		// The gate lands on the login that ran out of credits, so another login
		// of the same provider stays usable -- the same rule the timed path
		// follows.
		if want := "test@cp2"; rl[0].Subject != want {
			t.Errorf("Subject = %q, want %q", rl[0].Subject, want)
		}
		if !rl[0].Until.IsZero() {
			t.Errorf("Until = %v, want the zero value: a credit gate is until cleared", rl[0].Until)
		}
	})

	t.Run("ledger write failure still switches", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := creditSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = creditExit(t, rt, b, jsonlLine(t, "opencode-errors/results.jsonl", 1), 1)
		// A KV whose ledger put fails: the record cannot be written, so the
		// switch must proceed anyway -- the same contract the timed path keeps.
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

	t.Run("not switchable records nothing", func(t *testing.T) {
		// A round with no builder running cannot be switched, so there is
		// nothing to gate: the whole gate is skipped and the exit stands.
		fr := newFakeRunner()
		rt, b := creditSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = creditExit(t, rt, b, jsonlLine(t, "opencode-errors/results.jsonl", 1), 1)
		b.BuilderCandidate = ""
		assertTailReachesScan(t, rt, b)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
			t.Errorf("rate_limited entries = %+v, want none for a non-switchable round", rl)
		}
		if got.RoundExcluded != nil && len(got.RoundExcluded) != 0 {
			t.Errorf("RoundExcluded = %v, want empty", got.RoundExcluded)
		}
	})
}

// TestReconcileHeadlessCrashWithoutCreditStaysUntilCleared is case B: the pin.
// A builder that simply died, with nothing in its tail naming an empty balance,
// keeps every part of the exclusion behaviour -- RoundExcluded grows, the switch
// counts, and nothing is written to the ledger. It guards against the
// over-correction: a classifier loose enough to claim ordinary crashes would
// gate providers on a balance nothing said had run out.
func TestReconcileHeadlessCrashWithoutCreditStaysUntilCleared(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := creditSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = creditExit(t, rt, b, crashFixtureLine+"\n", 3)
	assertTailReachesScan(t, rt, b)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testOpencodeRef {
		t.Errorf("RoundExcluded = %v, want [%s]: a genuine crash stays excluded for the rest of the round", got.RoundExcluded, testOpencodeRef)
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
}

// TestCreditClassNeverGatesTimed is case C: the anti-regression guard. Every
// shape of the credit-exhaustion class the classifier is meant to know must
// land on the until-cleared gate with no exclusion and no switch charged. A
// future change that gives any of these a timed Until turns this red, by name.
func TestCreditClassNeverGatesTimed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"the real 402 fixture", jsonlLine(t, "opencode-errors/results.jsonl", 1)},
		{"requires more credits", "Error: this request requires more credits\n"},
		{"credits exhausted", "Error: credits exhausted for this account\n"},
		{"can only afford", "Error: this request exceeds what the key can only afford\n"},
		{"insufficient credits", "Error 402: insufficient credits, add funds to continue\n"},
		{"insufficient quota", "Error 429: insufficient quota for this key\n"},
		// The reset-like tails: the line names when it clears and the gate must
		// still not, because the provider promised a clock for the limit and
		// not for the top-up.
		{"credit line carrying a reset", "Error: this request requires more credits; the limit resets in 23m\n"},
		{"credit line carrying a clock", "Error: credits exhausted, resets at 23:30\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := newFakeRunner()
			rt, b := creditSetup(t, fr)
			rt = at(rt, 10*time.Minute)
			b = creditExit(t, rt, b, c.raw, 1)
			assertTailReachesScan(t, rt, b)

			got, err := reconcile(t, rt, b)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			rl := rateLimitedEntries(loadLedger(t, rt))
			if len(rl) != 1 {
				t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
			}
			if !rl[0].Until.IsZero() {
				t.Errorf("Until = %v, want the zero value: the credit class must never gate timed", rl[0].Until)
			}
			if rl[0].Note == "" {
				t.Error("Note is empty: the cause must be kept in the gate note")
			}
			if len(got.RoundExcluded) != 0 {
				t.Errorf("RoundExcluded = %v, want empty: the credit class never reaches the #191 exclusion", got.RoundExcluded)
			}
			if got.RoundSwitches != 0 {
				t.Errorf("RoundSwitches = %d, want 0 (uncounted)", got.RoundSwitches)
			}
		})
	}
}

// TestCreditNoMatchLeavesOtherGatesUntouched is the guard's other edge: a tail
// the classifier does not know must leave the other gates exactly as they were.
// It is what makes the table above mean something -- without it, "gate
// everything until cleared" would pass C too.
//
// The two timed rows are the ones that matter most here: a resetting weekly
// limit and a provider fault both keep their timed Until, because the credit
// class is narrower than "quota ran out" and widening it would make every 429
// and every 503 wait on a human.
func TestCreditNoMatchLeavesOtherGatesUntouched(t *testing.T) {
	t.Parallel()

	timed := []struct {
		name string
		raw  string
	}{
		{"a resetting weekly limit", "Error 429: You have reached your weekly ExamplePass limit. The limit resets in 23m, please try again later.\n"},
		{"a provider fault", "API error: UNAVAILABLE (code 503): no capacity\n"},
	}
	for _, c := range timed {
		t.Run(c.name, func(t *testing.T) {
			fr := newFakeRunner()
			rt, b := creditSetup(t, fr)
			rt = at(rt, 10*time.Minute)
			b = creditExit(t, rt, b, c.raw, 1)
			assertTailReachesScan(t, rt, b)

			got, err := reconcile(t, rt, b)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			rl := rateLimitedEntries(loadLedger(t, rt))
			if len(rl) != 1 {
				t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
			}
			if rl[0].Until.IsZero() {
				t.Error("Until is zero, want a timed gate: this shape is not the credit class")
			}
			if len(got.RoundExcluded) != 0 {
				t.Errorf("RoundExcluded = %v, want empty", got.RoundExcluded)
			}
			if got.RoundSwitches != 0 {
				t.Errorf("RoundSwitches = %d, want 0 (uncounted)", got.RoundSwitches)
			}
		})
	}

	// And the crash path: nothing claimed, so the exclusion stands untouched.
	for _, raw := range []string{
		crashFixtureLine + "\n",
		"",
		"fatal error: all goroutines are asleep - deadlock!\n",
	} {
		fr := newFakeRunner()
		rt, b := creditSetup(t, fr)
		rt = at(rt, 10*time.Minute)
		b = creditExit(t, rt, b, raw, 3)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile (tail %q): %v", raw, err)
		}
		if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testOpencodeRef {
			t.Errorf("tail %q: RoundExcluded = %v, want [%s]: an unknown tail must stay until-cleared", raw, got.RoundExcluded, testOpencodeRef)
		}
		if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
			t.Errorf("tail %q: rate_limited entries = %+v, want none", raw, rl)
		}
		if got.RoundSwitches != 1 {
			t.Errorf("tail %q: RoundSwitches = %d, want 1 (counted)", raw, got.RoundSwitches)
		}
	}
}

// TestCreditGateIsNotAReportPresentPath pins the last case in the plan: the
// credit classification keeps the report-on-disk branch. With a report on disk
// the gate is recorded but nothing is switched, because the round closes as it
// would have anyway.
func TestCreditGateIsNotAReportPresentPath(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := creditSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = creditExit(t, rt, b, jsonlLine(t, "opencode-errors/results.jsonl", 1), 1)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %+v, want none: a report on disk closes the round, nothing is switched", sw)
	}
}
