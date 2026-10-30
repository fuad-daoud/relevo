package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// candidateSetForCodes is a candidate set for the classification rows above: two
// providers, one of them the one the CLI's own example names, so a refusal's
// suggestion has something to suggest.
func candidateSetForCodes(t *testing.T) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[
	  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
	  {"harness":"opencode","provider":"cline-pass","model":"m","roles":["builder"]}
	]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	return set
}

// codedOf is the frame's code and next hint for err, or ok false when err is
// not a coded error at all.
func codedOf(t *testing.T, err error) (errorCode, string, bool) {
	t.Helper()
	var ce *cliError
	if !errors.As(err, &ce) {
		return "", "", false
	}
	return ce.code, ce.next, true
}

// TestBindInvalidNameIsUsage pins case 1: an invalid binding name is the
// caller's own --name, so it is usage with `relevo help` as the way out. Before
// the name errors carried a class it was internal, and `relevo bugreport` was
// the only hint a user was given for a typo.
func TestBindInvalidNameIsUsage(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		want string
	}{
		{"plB", `binding name "plB" has an invalid character "B"`},
		{"", "binding name is empty"},
		{"Plb", `binding name "Plb" must start with a lowercase letter`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			// The name is refused where it is validated, so the code is read
			// off the very error bind would return.
			err := store.ValidName(c.name)
			if err == nil {
				t.Fatalf("ValidName(%q) = nil, want a refusal", c.name)
			}
			coded, next, ok := codedOf(t, writeError(err))
			if !ok {
				t.Fatalf("writeError(%v) is not a coded error", err)
			}
			if coded != codeUsage {
				t.Errorf("code = %q, want %q", coded, codeUsage)
			}
			if next != "relevo help" {
				t.Errorf("next = %q, want %q", next, "relevo help")
			}
			if got := writeError(err).Error(); !strings.Contains(got, c.want) {
				t.Errorf("message = %q, want it to name %q", got, c.want)
			}
		})
	}
}

// TestSendBusyIsRefusedWithAWayOut pins case 3: a send against a round whose
// previous process is still alive is a refusal (exit 2) whose next hint names
// the way out, never internal.
func TestSendBusyIsRefusedWithAWayOut(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		err      error
		wantNext string
		wantText string
	}{
		{
			name:     "builder busy",
			err:      fmt.Errorf("binding %q (pid %d): %w", "plb", 4242, relevo.ErrBuilderBusy),
			wantNext: "relevo done",
			wantText: "binding \"plb\" (pid 4242)",
		},
		{
			name:     "report pending",
			err:      fmt.Errorf("binding %q round %d: report.md exists: %w", "plb", 2, relevo.ErrReportPending),
			wantNext: "relevo wait",
			wantText: "report.md exists",
		},
		{
			name:     "scope active",
			err:      fmt.Errorf("binding %q round %d: scope is still running: %w", "plb", 2, relevo.ErrScopeActive),
			wantNext: "relevo stop",
			wantText: "scope is still running",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := writeError(c.err)
			coded, next, ok := codedOf(t, got)
			if !ok {
				t.Fatalf("writeError(%v) is not a coded error", c.err)
			}
			if coded != codeRefused {
				t.Errorf("code = %q, want %q", coded, codeRefused)
			}
			if next != c.wantNext {
				t.Errorf("next = %q, want %q", next, c.wantNext)
			}
			if !strings.Contains(got.Error(), c.wantText) {
				t.Errorf("message %q must still name the binding and the reason", got.Error())
			}
			// A refused send earns exit 2, not the internal code's exit 1.
			if e := catalogExit(coded); e != 2 {
				t.Errorf("exit = %d, want 2", e)
			}
		})
	}
}

// TestBusySentinelsAreRefusals pins the class each busy sentinel carries at
// the point it is created, so a site that wraps one with %w is a refusal
// wherever it is classified -- not only in writeError's own ordered cases.
func TestBusySentinelsAreRefusals(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		sent error
	}{
		{"builder busy", relevo.ErrBuilderBusy},
		{"report pending", relevo.ErrReportPending},
		{"scope active", relevo.ErrScopeActive},
	} {
		wrapped := fmt.Errorf("binding %q (pid 1): %w", "plb", c.sent)
		if !errors.Is(wrapped, c.sent) {
			t.Errorf("%s: wrapping lost the specific sentinel", c.name)
		}
		if !errors.Is(wrapped, relevo.ErrRefused) {
			t.Errorf("%s: errors.Is(%v, ErrRefused) = false, want the refusal class", c.name, wrapped)
		}
	}
}

// TestUnknownGateSubjectIsUsage pins that a subject a caller named on the
// command line is refused as usage rather than reported as an internal failure:
// an unknown candidate token, a malformed ref and an unknown provider each
// carry the caller's own words plus the forms that are accepted, and none of
// them points at `relevo bugreport`. Dropping the case from writeError kills
// every row (the code becomes internal).
func TestUnknownGateSubjectIsUsage(t *testing.T) {
	t.Parallel()

	set := candidateSetForCodes(t)
	for _, c := range []struct {
		name    string
		subject string
	}{
		{"unknown token", "opencode/nope/m"},
		{"unknown name", "nope"},
		{"malformed ref", "opencode/test"},
		{"unknown provider", "clinepass"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, err := availability.ResolveClearSubject(set, availability.Ledger{}, c.subject)
			if err == nil {
				t.Fatalf("ResolveClearSubject(%q) = nil, want a refusal", c.subject)
			}
			got := writeError(err)
			coded, next, ok := codedOf(t, got)
			if !ok {
				t.Fatalf("writeError(%v) is not a coded error", err)
			}
			if coded != codeUsage {
				t.Errorf("code = %q, want %q", coded, codeUsage)
			}
			if next != "relevo help" {
				t.Errorf("next = %q, want %q", next, "relevo help")
			}
			if e := catalogExit(coded); e != 2 {
				t.Errorf("exit = %d, want 2", e)
			}
			if !strings.Contains(got.Error(), availability.AcceptedSubjects) {
				t.Errorf("message %q must name the accepted forms", got.Error())
			}
		})
	}
}

// TestOutOfRangeRoundIsRoundNotFound pins case 4: an out-of-range --round is a
// missing round, not a missing diff and not an internal failure.
func TestOutOfRangeRoundIsRoundNotFound(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		err  error
	}{
		{"range error", fmt.Errorf("round 5: binding has 2 rounds: %w", relevo.ErrRoundNotFound)},
		{"in-package constructor", relevo.RoundOutOfRange("plb", 5, 2, false)},
		{"drift constructor names --drift", relevo.RoundOutOfRange("webshop", 99, 2, true)},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := classifyReadErr(c.err)
			coded, next, ok := codedOf(t, got)
			if !ok {
				t.Fatalf("classifyReadErr(%v) is not a coded error", c.err)
			}
			if coded != codeRoundNotFound {
				t.Errorf("code = %q, want %q", coded, codeRoundNotFound)
			}
			if next != "relevo history" {
				t.Errorf("next = %q, want %q", next, "relevo history")
			}
			if e := catalogExit(coded); e != 1 {
				t.Errorf("exit = %d, want 1", e)
			}
		})
	}

	// The drift form must keep naming --drift, which the artifact error it
	// replaces did.
	if got := classifyReadErr(relevo.RoundOutOfRange("webshop", 99, 2, true)).Error(); !strings.Contains(got, "--drift") {
		t.Errorf("drift out-of-range message %q must name --drift", got)
	}
}

func TestWriteErrorClassifiesUserSideRefusals(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		err  error
		code errorCode
		next string
	}{
		{"candidate on open round", fmt.Errorf("send: %w", &relevo.CandidateRoundOpenError{Name: "smoke", Round: 1}), codeConflict, "relevo stop smoke"},
		{"no viable placement", fmt.Errorf("bind: %w", resolve.ErrNoViablePlacement), codePolicyRefused, "relevo config"},
		{"no mastermind session", fmt.Errorf("bind: %w", relevo.ErrNoMasterMindSession), codeUsage, "relevo mastermind init"},
		{"missing binding", fmt.Errorf("wait: %w", store.ErrNotFound), codeBindingNotFound, "relevo status --all"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			code, next, ok := codedOf(t, writeError(c.err))
			if !ok || code != c.code || next != c.next {
				t.Fatalf("writeError = (%q, %q, %v), want (%q, %q)", code, next, ok, c.code, c.next)
			}
		})
	}
}

func TestWaitFailureMapsOnlyNotFoundToBindingNotFound(t *testing.T) {
	t.Parallel()

	code, next, ok := codedOf(t, waitFailure(fmt.Errorf("smoke: %w", store.ErrNotFound)))
	if !ok || code != codeBindingNotFound || next != "relevo status --all" {
		t.Fatalf("waitFailure(ErrNotFound) = (%q, %q, %v)", code, next, ok)
	}
	code, _, ok = codedOf(t, waitFailure(errors.New("boom")))
	if !ok || code != codeInternal {
		t.Fatalf("waitFailure(other) = (%q, %v), want internal", code, ok)
	}
}
