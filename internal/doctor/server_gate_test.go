package doctor

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestServerGateCheckNamesGatedCandidates pins the row: the gated tokens,
// with the names the server knows when there are any, and nothing for the
// ungated.
func TestServerGateCheckNamesGatedCandidates(t *testing.T) {
	t.Parallel()

	resp := remote.CandidatesResponse{Candidates: []remote.CandidateView{
		{Token: "agy/openai/gpt-4", Name: "gpt-4", Kind: "agy", Gated: true},
		{Token: "claude/anthropic/haiku", Kind: "claude", Gated: false},
		{Token: "opencode/laptop/qwen", Kind: "opencode", Gated: true},
	}}
	c, ok := serverGateCheck("zen", resp, nil)
	if !ok {
		t.Fatal("serverGateCheck = not ok, want the gated row")
	}
	want := "zen: gated: agy/openai/gpt-4 (gpt-4), opencode/laptop/qwen"
	if c.Detail != want {
		t.Errorf("Detail = %q, want %q", c.Detail, want)
	}
	if c.Severity != SevWarn {
		t.Errorf("Severity = %v, want warn: a gated provider is a fact, not a failure", c.Severity)
	}
	if c.Name != "servers" {
		t.Errorf("Name = %q, want servers", c.Name)
	}
}

// TestServerGateCheckEmptyReportsNothing pins the quiet case: no gated
// candidates and no error is no row.
func TestServerGateCheckEmptyReportsNothing(t *testing.T) {
	t.Parallel()

	resp := remote.CandidatesResponse{Candidates: []remote.CandidateView{
		{Token: "claude/anthropic/haiku", Name: "haiku", Kind: "claude", Gated: false},
	}}
	if c, ok := serverGateCheck("zen", resp, nil); ok {
		t.Errorf("serverGateCheck = %+v, true; want no row when nothing is gated", c)
	}
}

// TestServerGateCheckErrorDegrades pins the degraded line: a candidates
// failure reads as unreachable and never blocks the report.
func TestServerGateCheckErrorDegrades(t *testing.T) {
	t.Parallel()

	c, ok := serverGateCheck("zen", remote.CandidatesResponse{}, errors.New("dial tcp: connection refused"))
	if !ok {
		t.Fatal("serverGateCheck = not ok, want the degraded row")
	}
	if c.Detail != "zen: unreachable: dial tcp: connection refused" {
		t.Errorf("Detail = %q, want the unreachable line", c.Detail)
	}
	if !c.ProbeFailed {
		t.Error("ProbeFailed = false, want true: relevo could not establish the fact")
	}
}
