package availability

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
)

// availTwoProviderJSON has candidates on the provider the CLI's own example
// uses, so a refusal's "known:" list matches the design's sample.
const availTwoProviderJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]},
  {"harness":"opencode","provider":"cline-pass","model":"m","roles":["builder"]}
]`

// clearSubjectCase is one TestResolveClearSubject row.
type clearSubjectCase struct {
	name       string
	set        *candidate.Set
	l          Ledger
	subject    string
	want       string
	wantErr    error
	wantMsg    string // exact Error() text, when non-empty
	wantSubstr string // substring Error() must contain, when non-empty
	notSubstr  string // substring Error() must not contain, when non-empty
}

// TestResolveClearSubject: a subject is known when it names a
// configured candidate or provider, or a provider the ledger still gates --
// which is how a gate left behind by a candidate removed from
// candidates.json stays clearable. Everything else is refused.
func TestResolveClearSubject(t *testing.T) {
	t.Parallel()

	twoProviders := candidateSet(t, availTwoProviderJSON)
	testOnly := candidateSet(t, `[{"harness":"opencode","provider":"test","model":"m","roles":["builder"]}]`)
	goneGate := Ledger{Entries: []Entry{
		{Kind: RateLimited, Subject: "gone", At: baseTime, Source: "planner"},
	}}

	tests := []clearSubjectCase{
		{
			name: "configured token", set: twoProviders, subject: "claude/test/m",
			want: "test",
		},
		{
			name: "bare provider", set: twoProviders, subject: "cline-pass",
			want: "cline-pass",
		},
		{
			name: "bare provider typo", set: twoProviders, subject: "clinepass",
			wantErr: ErrUnknownProvider,
			wantMsg: `no configured candidate uses provider "clinepass" (known: cline-pass, test); did you mean "cline-pass"?`,
		},
		{
			name: "bare provider unknown", set: twoProviders, subject: "zzz",
			wantErr: ErrUnknownProvider, notSubstr: "did you mean",
		},
		{
			name: "gate left by a removed candidate", set: testOnly, l: goneGate, subject: "gone",
			want: "gone",
		},
		{
			name: "token of a gate left behind", set: testOnly, l: goneGate, subject: "claude/gone/m",
			want: "gone",
		},
		{
			name: "unknown token", set: testOnly, subject: "claude/nope/m",
			wantErr: candidate.ErrUnknownCandidate,
		},
		{
			name: "nil set, bare subject", subject: "anything",
			wantErr: ErrUnknownProvider, wantSubstr: "(no candidates configured)",
		},
		{
			name: "nil set, token", subject: "claude/x/m",
			wantErr: candidate.ErrUnknownCandidate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveClearSubject(tt.set, tt.l, tt.subject)
			checkClearSubject(t, tt, got, err)
		})
	}
}

// checkClearSubject asserts one clear-subject outcome.
func checkClearSubject(t *testing.T, tt clearSubjectCase, got string, err error) {
	t.Helper()
	if tt.wantErr != nil {
		if !errors.Is(err, tt.wantErr) {
			t.Fatalf("err = %v, want %v", err, tt.wantErr)
		}
	} else if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != tt.want {
		t.Errorf("provider = %q, want %q", got, tt.want)
	}
	if err == nil {
		if tt.wantMsg != "" || tt.wantSubstr != "" || tt.notSubstr != "" {
			t.Fatalf("message assertions need an error, got provider %q", got)
		}
		return
	}
	if tt.wantMsg != "" && err.Error() != tt.wantMsg {
		t.Errorf("message = %q, want %q", err.Error(), tt.wantMsg)
	}
	if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
		t.Errorf("message = %q, want it containing %q", err.Error(), tt.wantSubstr)
	}
	if tt.notSubstr != "" && strings.Contains(err.Error(), tt.notSubstr) {
		t.Errorf("message = %q, want no %q", err.Error(), tt.notSubstr)
	}
}

// TestSuggestProvider: a refused subject offers the closest known provider
// when one is close enough to be worth naming.
func TestSuggestProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		known   []string
		subject string
		want    string
	}{
		{"normalised match", []string{"cline-pass", "test"}, "clinepass", "cline-pass"},
		{"edit distance two", []string{"openai", "test"}, "openia", "openai"},
		{"nothing close", []string{"cline-pass", "test"}, "zzz", ""},
		{"tie takes the alphabetically first", []string{"openai", "openaa"}, "openab", "openaa"},
		{"no known providers", nil, "anything", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := suggestProvider(tt.known, tt.subject); got != tt.want {
				t.Errorf("suggestProvider(%v, %q) = %q, want %q", tt.known, tt.subject, got, tt.want)
			}
		})
	}
}

// TestResolveClearSubjectName: `gate --clear <name>` clears that
// candidate's provider, and a bare provider still clears the provider.
func TestResolveClearSubjectName(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testTwoProviderJSON)

	for _, tt := range []struct {
		subject string
		want    string
	}{
		{"m", "test"},
		{"agy-m", "other"},
	} {
		provider, err := ResolveClearSubject(set, Ledger{}, tt.subject)
		if err != nil {
			t.Fatalf("ResolveClearSubject(%s): %v", tt.subject, err)
		}
		if provider != tt.want {
			t.Errorf("ResolveClearSubject(%s) = %q, want %q", tt.subject, provider, tt.want)
		}
	}

	provider, err := ResolveClearSubject(set, Ledger{}, "other")
	if err != nil {
		t.Fatalf("ResolveClearSubject(other): %v", err)
	}
	if provider != "other" {
		t.Errorf("ResolveClearSubject(other) = %q, want other", provider)
	}
}
