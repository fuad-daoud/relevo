package setup

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// codexInventory names a two-model codex inventory the builds share.
func codexInventory() Inventory {
	return Inventory{Harnesses: []HarnessStatus{
		status("codex", true, "gpt-6-luna", "gpt-5.6-terra"),
	}}
}

// parseActorsOf decodes the actors section a Files carries.
func parseActorsOf(t *testing.T, files Files) map[string]roles.Actor {
	t.Helper()
	actorSet, _, err := roles.ParseActors(files.Actors)
	if err != nil {
		t.Fatalf("roles.ParseActors: %v\n%s", err, files.Actors)
	}
	return actorSet
}

// candidatesOf decodes the candidates section a Files carries.
func candidatesOf(t *testing.T, files Files) []candidate.Candidate {
	t.Helper()
	var cands []candidate.Candidate
	if err := json.Unmarshal(files.Candidates, &cands); err != nil {
		t.Fatalf("unmarshal candidates: %v\n%s", err, files.Candidates)
	}
	return cands
}

// TestBuildDerivesReviewerSecurityLitePlanner pins the chain's own reader
// picks: the reviewer and lite-planner over the codex model the offer list
// lists last, and security over the planner's candidate -- the planner's
// model at high, a candidate apart from the builder's medium.
func TestBuildDerivesReviewerSecurityLitePlanner(t *testing.T) {
	rec := RecommendedAnswers(codexInventory(), nil)
	files, err := Build(codexInventory(), rec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	actorSet := parseActorsOf(t, files)
	for _, tt := range []struct{ actor, want string }{
		{"builder", "gpt-6-luna"},
		{"librarian", "gpt-6-luna"},
		{"planner", "gpt-6-luna-high"},
		{"reviewer", "gpt-5.6-terra"},
		{"security", "gpt-6-luna-high"},
		{"lite-planner", "gpt-5.6-terra"},
	} {
		a, ok := actorSet[tt.actor]
		if !ok {
			t.Fatalf("actors %v miss %s", keysOf(actorSet), tt.actor)
		}
		names := actorNames(a)
		if want := []string{tt.want}; !reflect.DeepEqual(names, want) {
			t.Errorf("%s candidates = %v, want %v", tt.actor, names, want)
		}
	}
	if got := reflect.DeepEqual(files.ActorOrder, chainOrder); !got {
		t.Errorf("ActorOrder = %v, want %v", files.ActorOrder, chainOrder)
	}
	var names []string
	for _, c := range candidatesOf(t, files) {
		names = append(names, c.Model)
	}
	if want := []string{"gpt-6-luna:medium", "gpt-6-luna:high", "gpt-5.6-terra:medium"}; !reflect.DeepEqual(names, want) {
		t.Errorf("candidates' models = %v, want %v", names, want)
	}
}

// TestBuildSharedModelIsOneCandidate pins the sharing rule: a model picked
// twice names one candidate that every actor of the run lists.
func TestBuildSharedModelIsOneCandidate(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{status("claude", true, "sonnet", "opus", "haiku")}}
	answers := Answers{
		"builder": {"claude/anthropic/sonnet"},
		"planner": {"claude/anthropic/sonnet"},
		"tier":    {"yolo"},
	}
	files, err := Build(inv, answers)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cands := candidatesOf(t, files)
	if len(cands) != 1 || cands[0].Model != "sonnet" {
		t.Fatalf("candidates = %v, want the one sonnet candidate", files.CandidateNames)
	}
	actorSet := parseActorsOf(t, files)
	for _, name := range []string{"builder", "librarian", "planner", "reviewer", "security", "lite-planner"} {
		if got := actorNames(actorSet[name]); !reflect.DeepEqual(got, []string{"sonnet"}) {
			t.Errorf("%s candidates = %v, want the one shared candidate", name, got)
		}
	}
}

// TestBuildTierFlowsToPolicyAndWriters pins the tier's walk: the policy's
// ceiling and both writer actors carry it, the readers carry nothing.
func TestBuildTierFlowsToPolicyAndWriters(t *testing.T) {
	answers := Answers{
		"builder": {"codex/openai/gpt-6-luna:high"},
		"planner": {"codex/openai/gpt-6-luna:high"},
		"tier":    {"edit"},
	}
	files, err := Build(codexInventory(), answers)
	if err != nil {
		t.Fatalf("Build(edit): %v", err)
	}
	actorSet := parseActorsOf(t, files)
	if actorSet["builder"].Tier != "edit" || actorSet["librarian"].Tier != "edit" {
		t.Errorf("writers' tiers = %q/%q, want edit", actorSet["builder"].Tier, actorSet["librarian"].Tier)
	}
	for _, name := range []string{"planner", "reviewer", "security", "lite-planner"} {
		if actorSet[name].Tier != "" {
			t.Errorf("%s tier = %q, want none", name, actorSet[name].Tier)
		}
	}
	pol, err := policyFrom(files.Policy)
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}
	if pol.MaxTier != "edit" {
		t.Errorf("policy MaxTier = %q, want edit", pol.MaxTier)
	}

	answers["tier"] = []string{"yolo"}
	files, err = Build(codexInventory(), answers)
	if err != nil {
		t.Fatalf("Build(yolo): %v", err)
	}
	actorSet = parseActorsOf(t, files)
	if actorSet["builder"].Tier != "yolo" {
		t.Errorf("builder tier = %q, want yolo", actorSet["builder"].Tier)
	}
}

// TestBuildCandidatesCarryNoRolesKey keeps today's pin: the candidates the
// questions build name no roles and no tier; the upscale decisions stay the
// actors' alone.
func TestBuildCandidatesCarryNoRolesKey(t *testing.T) {
	files, err := Build(codexInventory(), RecommendedAnswers(codexInventory(), nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, key := range []string{`"roles"`, `"tier"`} {
		if strings.Contains(string(files.Candidates), key) {
			t.Errorf("candidates JSON carries %s:\n%s", key, files.Candidates)
		}
	}
	cands := candidatesOf(t, files)
	for _, c := range cands {
		if c.Roles != nil || c.Tier != "" {
			t.Errorf("candidate %s carries roles %v / tier %q, want neither", c.Ref(), c.Roles, c.Tier)
		}
	}
}

// TestBuildLibrarianScopePinsDocsOnly pins the librarian's seed: the same
// names as the builders, a docs-only scope with comments on, and a nil check
// so the writer default (on) applies.
func TestBuildLibrarianScopePinsDocsOnly(t *testing.T) {
	files, err := Build(codexInventory(), RecommendedAnswers(codexInventory(), nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	actorSet := parseActorsOf(t, files)
	librarian := actorSet["librarian"]
	if want := (&pathscope.Scope{Paths: []string{"@docs"}, Comments: true}); !reflect.DeepEqual(librarian.Scope, want) {
		t.Errorf("librarian scope = %+v, want %+v", librarian.Scope, want)
	}
	if librarian.Check != nil {
		t.Errorf("librarian check = %v, want nil (the writer default)", *librarian.Check)
	}
	if !reflect.DeepEqual(librarian.Candidates, actorSet["builder"].Candidates) {
		t.Errorf("librarian candidates = %+v, want the builders' %v", librarian.Candidates, actorSet["builder"].Candidates)
	}
}

// policyFrom decodes the policy section a Files carries.
func policyFrom(data []byte) (policy.Policy, error) {
	var p policy.Policy
	err := json.Unmarshal(data, &p)
	return p, err
}

func keysOf(set map[string]roles.Actor) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

func actorNames(a roles.Actor) []string {
	names := make([]string, 0, len(a.Candidates))
	for _, e := range a.Candidates {
		names = append(names, e.Candidate)
	}
	return names
}
