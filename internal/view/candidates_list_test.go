package view

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// Every expectation below is built from the names DeriveNames gives
// testCandidatesJSON's entries (agy/test/m -> agy-m, claude/test/m ->
// claude-m, opencode/test/m -> m): the name column is sized from the names,
// and the token follows as plain text in its own column. Round 3 F1 removed
// the faint wrapper, so the block carries no escapes.

func TestFormatCandidates(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	got := FormatCandidates(set, nil)
	want := "agy-m" + strings.Repeat(" ", 5) + "agy/test/m     " + "  builder   [--dangerously-skip-permissions]   note: extra_args carries --dangerously-skip-permissions; launches at tier harness only -- move it to \"tier\"\n" +
		"claude-m" + strings.Repeat(" ", 2) + "claude/test/m  " + "  builder, reviewer\n" +
		"m" + strings.Repeat(" ", 9) + "opencode/test/m" + "  builder\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesEmpty(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, "[]")
	got := FormatCandidates(set, nil)
	want := "no candidates configured; set one with relevo config set candidates (see README \"Candidates\")\n"
	if got != want {
		t.Errorf("FormatCandidates(empty) = %q, want %q", got, want)
	}
}

func TestFormatCandidatesMarksGated(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	gates := []availability.Gate{
		{Token: testClaudeRef, Kind: availability.RateLimited, Until: time.Time{}},
	}
	got := FormatCandidates(set, gates)
	want := "agy-m" + strings.Repeat(" ", 5) + "agy/test/m     " + "  builder   [--dangerously-skip-permissions]   note: extra_args carries --dangerously-skip-permissions; launches at tier harness only -- move it to \"tier\"\n" +
		"claude-m" + strings.Repeat(" ", 2) + "claude/test/m  " + "  builder, reviewer   unavailable: rate-limited until cleared\n" +
		"m" + strings.Repeat(" ", 9) + "opencode/test/m" + "  builder\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesTier(t *testing.T) {
	t.Parallel()

	json := `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"yolo"},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
]`
	set := candidateSet(t, json)
	got := FormatCandidates(set, nil)
	// claude's entry takes "m" first, so agy's becomes "agy-m".
	want := "agy-m" + strings.Repeat(" ", 2) + "agy/test/m   " + "  builder   [--dangerously-skip-permissions]   note: extra_args carries --dangerously-skip-permissions; launches at tier harness only -- move it to \"tier\"\n" +
		"m" + strings.Repeat(" ", 6) + "claude/test/m" + "  builder   tier: yolo\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesLatencySuffix(t *testing.T) {
	t.Parallel()

	const twoCandidates = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`
	set := candidateSet(t, twoCandidates)
	lat := map[string]availability.Summary{"claude/test/m": {N: 3, TTFTP50MS: 640}}

	got := FormatCandidatesLatency(set, nil, lat)
	want := "m" + strings.Repeat(" ", 11) + "claude/test/m  " + "  builder   ttft p50 640ms (n=3, 30d)\n" +
		"opencode-m" + strings.Repeat(" ", 2) + "opencode/test/m" + "  builder\n"
	if got != want {
		t.Errorf("FormatCandidatesLatency() =\n%q\nwant:\n%q", got, want)
	}

	// The token with no samples renders exactly as the plain listing does.
	other := got[strings.Index(got, "\n")+1:]
	plainList := FormatCandidates(set, nil)
	plain := plainList[strings.Index(plainList, "\n")+1:]
	if other != plain {
		t.Errorf("unprobed line = %q, want FormatCandidates's %q", other, plain)
	}

	// With no history at all, the two render identically.
	if a, b := FormatCandidatesLatency(set, nil, nil), FormatCandidates(set, nil); a != b {
		t.Errorf("FormatCandidatesLatency(no history) = %q, want %q", a, b)
	}
}

// TestCandidateRoles pins the registry lookup the candidates detail block
// reads: the roles come from the registry, in reg.Names() order, and an empty
// registry with no set -- nothing to compare against -- answers nil instead of
// panicking.
func TestCandidateRoles(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	reg, err := roles.Build(nil, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	got := CandidateRoles(reg, set, testOpencodeRef)
	if strings.Join(got, ", ") != "builder" {
		t.Errorf("CandidateRoles(%s) = %v, want [builder]", testOpencodeRef, got)
	}

	empty, err := roles.Build(nil, candidateSet(t, "[]"), policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build(empty): %v", err)
	}
	if got := CandidateRoles(empty, nil, testOpencodeRef); got != nil {
		t.Errorf("CandidateRoles(empty registry, nil set) = %v, want nil", got)
	}
}

// TestFormatCandidatesHasNoEscapes pins round 3 F1: a mastermind reads `relevo
// config` through a pipe, so the candidates block must be plain text -- no SGR
// sequence anywhere in it.
func TestFormatCandidatesHasNoEscapes(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef, testAgyRef}},
		"reviewer": {Candidates: []string{testClaudeRef}},
	})

	for _, got := range []string{
		FormatCandidatesLatencyFor(reg, set, nil, nil),
		FormatCandidates(set, nil),
	} {
		if strings.Contains(got, "\x1b") {
			t.Errorf("candidate listing contains an escape sequence:\n%q", got)
		}
	}
}
