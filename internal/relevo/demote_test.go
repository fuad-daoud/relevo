package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
)

func demoteGate(token string) availability.Gate {
	return availability.Gate{Token: token, Kind: availability.Scheduled, Note: "demoted until 14:00Z", Demote: true}
}

func demoteRegistry(t *testing.T) (*candidate.Set, *roles.Registry) {
	t.Helper()
	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/a", "claude/test/b", "claude/test/c"}},
	})
	return set, reg
}

func TestDemotedCandidateMovesLast(t *testing.T) {
	t.Parallel()

	set, reg := demoteRegistry(t)
	gates := []availability.Gate{demoteGate("claude/test/b")}

	res, err := resolve.ResolveRole(reg, set, gates, "", "builder")
	if err != nil || res.Candidate.Name != "a" {
		t.Fatalf("pick = %v, %v; want a", res.Candidate.Name, err)
	}

	gated := append([]availability.Gate{{Token: "claude/test/a", Kind: availability.RateLimited}}, gates...)
	res, err = resolve.ResolveRole(reg, set, gated, "", "builder")
	if err != nil || res.Candidate.Name != "c" {
		t.Fatalf("pick with a gated = %v, %v; want c, not the demoted b", res.Candidate.Name, err)
	}
}

func TestDemoteNeverRefusesTheSoleCandidate(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/a"}},
	})
	gates := []availability.Gate{demoteGate("claude/test/a")}

	res, err := resolve.ResolveRole(reg, set, gates, "", "builder")
	if err != nil || res.Candidate.Name != "a" {
		t.Fatalf("pick = %v, %v; want a", res.Candidate.Name, err)
	}
	if got := RoleRefusalsFor(reg, set, policy.Policy{}, gates); len(got) != 0 {
		t.Errorf("RoleRefusalsFor = %v, want none", got)
	}
}

func TestDemoteRowShowsAtTheDemotedPosition(t *testing.T) {
	t.Parallel()

	set, reg := demoteRegistry(t)
	gates := []availability.Gate{demoteGate("claude/test/a")}

	got := FormatPolicyFor(reg, nil, set, policy.Policy{}, gates, nil, availability.History{}, baseTime, time.UTC)

	b := strings.Index(got, "  1  b")
	c := strings.Index(got, "  2  c")
	a := strings.Index(got, "  a  ")
	if b < 0 || c < 0 || a < 0 || !(b < c) {
		t.Fatalf("rows not reordered:\n%s", got)
	}
	if !strings.Contains(got, "<- would pick") || !strings.Contains(got, "demoted until 14:00Z") {
		t.Errorf("want pick marker and window text:\n%s", got)
	}
	if !strings.Contains(got[:strings.Index(got, "librarian")], "b  order     <- would pick") {
		t.Errorf("marker should follow the pick b:\n%s", got)
	}
}
