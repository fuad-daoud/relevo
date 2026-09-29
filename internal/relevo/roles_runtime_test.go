package relevo

// The file-mode tests for #374 round 2b: every runtime path that launches or
// picks for a role asks the registry, and these pin what changes when that
// registry came from roles.json. The legacy behaviour is pinned by the older
// tests, which stay unedited.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// rolesRuntimeCandidatesJSON is three claude candidates, all able to serve
// builder: the file's rows, not candidates.json's roles, decide who is picked.
const rolesRuntimeCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"b","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"c","roles":["builder"]}
]`

// rolesRuntimeTierCandidatesJSON is the same set with b carrying a candidate
// tier of its own, which file mode ignores.
const rolesRuntimeTierCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"b","roles":["builder"],"tier":"yolo"}
]`

// rolesFileRegistry builds the registry cmd/relevo builds from roles.json,
// from in-memory rows over set (#374 §9 step 6).
func rolesFileRegistry(t *testing.T, set *candidate.Set, pol policy.Policy, rows map[string]roles.Row) *roles.Registry {
	t.Helper()
	reg, err := roles.Build(&roles.File{Rows: rows}, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return reg
}

// actorsFileRegistry is rolesFileRegistry with the file's source set to the
// actors section, so the views label the candidates' section "config actors"
// (A2 round 2).
func actorsFileRegistry(t *testing.T, set *candidate.Set, pol policy.Policy, rows map[string]roles.Row) *roles.Registry {
	t.Helper()
	reg, err := roles.Build(&roles.File{Rows: rows, Source: roles.SourceActors}, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return reg
}

// rolesCandidate looks up one configured candidate by its token.
func rolesCandidate(t *testing.T, set *candidate.Set, token string) candidate.Candidate {
	t.Helper()
	ref, err := candidate.ParseRef(token)
	if err != nil {
		t.Fatalf("candidate.ParseRef(%q): %v", token, err)
	}
	c, err := set.Lookup(ref)
	if err != nil {
		t.Fatalf("set.Lookup(%q): %v", token, err)
	}
	return c
}

// TestRolesRuntimeFileModeRanking pins §4.3/§9 step 6.1: the row's candidates
// list is the ranked order, the position is the token's 1-based index in that
// list, a gated candidate is skipped and named, and a candidate the row does
// not list is never picked.
func TestRolesRuntimeFileModeRanking(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/b", "claude/test/a"}},
	})

	res, err := resolveRole(reg, set, nil, "", "builder")
	if err != nil {
		t.Fatalf("resolveRole: %v", err)
	}
	if got := res.Token(); got != "claude/test/b" {
		t.Errorf("picked %q, want claude/test/b", got)
	}
	if res.How != HowOrder || res.Position != 1 {
		t.Errorf("How/Position = %q/%d, want %q/1", res.How, res.Position, HowOrder)
	}

	// Gate the first choice: the walk takes the second, at its own position.
	gates := []availability.Gate{{
		Token: "claude/test/b", Kind: availability.RateLimited, Until: baseTime.Add(time.Hour),
	}}
	res, err = resolveRole(reg, set, gates, "", "builder")
	if err != nil {
		t.Fatalf("resolveRole with b gated: %v", err)
	}
	if got := res.Token(); got != "claude/test/a" {
		t.Errorf("picked %q, want claude/test/a", got)
	}
	if res.How != HowOrder || res.Position != 2 {
		t.Errorf("How/Position = %q/%d, want %q/2", res.How, res.Position, HowOrder)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Token != "claude/test/b" {
		t.Errorf("Skipped = %+v, want claude/test/b", res.Skipped)
	}

	// c is not in the row's list, so it can never be picked.
	if reg.Serves("builder", candidate.Ref{Harness: "claude", Provider: "test", Model: "c"}) {
		t.Error("Serves(builder, claude/test/c) = true, want false: the row does not list it")
	}
	both := []availability.Gate{
		{Token: "claude/test/b", Kind: availability.RateLimited, Until: baseTime.Add(time.Hour)},
		{Token: "claude/test/a", Kind: availability.RateLimited, Until: baseTime.Add(time.Hour)},
	}
	_, err = resolveRole(reg, set, both, "", "builder")
	if !errors.Is(err, ErrAllGated) {
		t.Fatalf("resolveRole with both listed candidates gated err = %v, want ErrAllGated", err)
	}
	if strings.Contains(err.Error(), "claude/test/c") {
		t.Errorf("err = %q, want it not to name the unlisted c", err.Error())
	}
}

// TestRolesRuntimeFileModeExplicitTokenNotListed pins §4.3/§9 step 6.2: an
// explicit token the row does not list is refused, and in file mode the
// refusal says so.
func TestRolesRuntimeFileModeExplicitTokenNotListed(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/b", "claude/test/a"}},
	})

	_, err := resolveRole(reg, set, nil, "claude/test/c", "builder")
	if err == nil {
		t.Fatal("resolveRole accepted an unlisted explicit token, want ErrRoleNotServed")
	}
	if !errors.Is(err, ErrRoleNotServed) {
		t.Errorf("err = %v, want ErrRoleNotServed", err)
	}
	if !strings.Contains(err.Error(), "roles.json") {
		t.Errorf("err = %q, want it to name roles.json", err.Error())
	}
}

// TestRolesRuntimeFileModeTier pins §4.4/§9 step 6.3: in file mode the role's
// row tier wins and the candidate's own tier is ignored; the explicit tier
// still wins over both.
func TestRolesRuntimeFileModeTier(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeTierCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {
			Tier:       ptr("edit"),
			Candidates: []string{"claude/test/b"},
		},
	})
	b := rolesCandidate(t, set, "claude/test/b")
	if b.Tier != "yolo" {
		t.Fatalf("test candidate tier = %q, want yolo", b.Tier)
	}

	if got := resolveRoleTier("", b, reg, "builder"); got != harness.TierEdit {
		t.Errorf("resolveRoleTier(\"\", b) = %q, want edit: the candidate tier is ignored in file mode", got)
	}
	if got := resolveRoleTier("read", b, reg, "builder"); got != harness.TierRead {
		t.Errorf("resolveRoleTier(\"read\", b) = %q, want read", got)
	}
}

// TestRolesRuntimeVerifyTier pins §4.6/§9 step 6.4: verifyTier reads the
// reviewer row in file mode, and gives yolo when the file sets no tier.
func TestRolesRuntimeVerifyTier(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeTierCandidatesJSON)
	c := rolesCandidate(t, set, "claude/test/b")

	rt := newRuntime(t)
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/b"}},
	})
	if got := verifyTier(rt, c); got != harness.TierYolo {
		t.Errorf("verifyTier with no reviewer tier = %q, want yolo", got)
	}

	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{"claude/test/b"}},
		"reviewer": {Tier: ptr("edit")},
	})
	if got := verifyTier(rt, c); got != harness.TierEdit {
		t.Errorf("verifyTier with reviewer tier edit = %q, want edit", got)
	}
}

// TestRolesRuntimeCustomBuilderLaunchesCustomAgent pins §5/§9 step 6.5: the
// builder launch spec comes from the registry, so a custom definition is what
// the argv carries, while a kind the file does not override keeps the shipped
// one.
func TestRolesRuntimeCustomBuilderLaunchesCustomAgent(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	set := rt.Candidates
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {
			Candidates:  []string{testClaudeRef, testOpencodeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "my-executor"}},
		},
	})

	fr := newFakeRunner()
	rt.Runner = fr
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "custom-builder", Candidate: testClaudeRef, MasterMindID: testMasterMindName,
		CWD: "/custom-repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := startRound(context.Background(), rt, nil, b, "the prompt", false); err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	if !containsAdjacentPair(fr.specs[0].Argv, "--agent", "my-executor") {
		t.Errorf("argv = %v, want --agent my-executor", fr.specs[0].Argv)
	}

	// A kind the row does not override keeps its shipped definition.
	fr2 := newFakeRunner()
	rt.Runner = fr2
	b2, err := Bind(context.Background(), rt, BindOptions{
		Name: "shipped-builder", Candidate: testOpencodeRef, MasterMindID: testMasterMindName,
		CWD: "/shipped-repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind opencode: %v", err)
	}
	if _, err := startRound(context.Background(), rt, nil, b2, "the prompt", false); err != nil {
		t.Fatalf("startRound opencode: %v", err)
	}
	if len(fr2.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr2.specs)
	}
	if !containsAdjacentPair(fr2.specs[0].Argv, "--agent", "plan-executor") {
		t.Errorf("argv = %v, want --agent plan-executor", fr2.specs[0].Argv)
	}
}

// TestRolesRuntimeFileModeProbe pins §5's probe paragraph for file mode: the
// probed role is whichever role's candidate list names the candidate, and a
// candidate no role lists is still "no known role".
func TestRolesRuntimeFileModeProbe(t *testing.T) {
	t.Parallel()

	rt, now := probeRuntime(t, testCandidatesJSON)
	set := rt.Candidates
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"security-reviewer": {
			Shape:       ptr("reader"),
			Definitions: map[string]roles.DefRow{"claude": {Agent: "sec-review"}},
			Candidates:  []string{testClaudeRef},
		},
	})

	fake := &fakeExec{now: now}
	got := availability.ProbeCandidate(context.Background(), AvailabilityDeps(rt), fake, rolesCandidate(t, set, testClaudeRef), "box")
	if len(fake.argvs) != 1 {
		t.Fatalf("probe ran %d times, want 1 (Err = %q)", len(fake.argvs), got.Err)
	}
	if !containsAdjacentPair(fake.argvs[0], "--agent", "sec-review") {
		t.Errorf("argv = %v, want --agent sec-review", fake.argvs[0])
	}

	orphan := candidate.Candidate{Harness: "claude", Provider: "test", Model: "orphan"}
	fake2 := &fakeExec{now: now}
	got2 := availability.ProbeCandidate(context.Background(), AvailabilityDeps(rt), fake2, orphan, "box")
	if got2.Err != "no known role" {
		t.Errorf("Err = %q, want %q", got2.Err, "no known role")
	}
	if fake2.calls != 0 {
		t.Errorf("fake was called %d times, want 0", fake2.calls)
	}
}
