package relevo

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// directFixDef returns the shipped direct-fix definition the funding tests
// compare against the default.
func directFixDef(t *testing.T) workflow.Definition {
	t.Helper()
	def, ok := workflow.Shipped("direct-fix")
	if !ok {
		t.Fatal("workflow.Shipped(direct-fix) = false, want the shipped definition")
	}
	return def
}

// TestChainParamsPrecedence pins the order chainParamsFor resolves a param in:
// the workflow default, then the repository entry, then policy, then an old
// flag, then --param. A flag must beat the repo and the policy, and --param
// must beat the flag. Swapping the repo and policy tiers is the mutation
// this test catches.
func TestChainParamsPrecedence(t *testing.T) {
	def := workflow.Default()
	pol := policy.Policy{Chain: &policy.ChainPolicy{ReviewerActor: "policy-reviewer"}}
	repo := RepoSettings{BuilderActor: "repo-builder", ReviewerActor: "repo-reviewer", PlannerActor: "repo-planner"}

	// Policy fills an unflagged slot the repository leaves alone.
	got, _, err := chainParamsFor(def, pol, ChainOptions{}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(policy): %v", err)
	}
	if got["reviewer"] != "policy-reviewer" {
		t.Errorf("policy tier: reviewer = %q, want policy-reviewer", got["reviewer"])
	}
	if _, ok := got["builder"]; ok {
		t.Errorf("policy tier: builder = %q, want no builder slot (the builder has no policy default)", got["builder"])
	}

	// The repository beats the policy, and fills the builder slot.
	got, _, err = chainParamsFor(def, pol, ChainOptions{}, repo)
	if err != nil {
		t.Fatalf("chainParamsFor(repo): %v", err)
	}
	for param, want := range map[string]string{
		"builder": "repo-builder", "reviewer": "repo-reviewer", "planner": "repo-planner",
	} {
		if got[param] != want {
			t.Errorf("repo tier: %s = %q, want %q (the repo must beat the policy)", param, got[param], want)
		}
	}

	// An old flag beats the repository's own value.
	got, _, err = chainParamsFor(def, pol, ChainOptions{ReviewerActor: "flag-reviewer"}, repo)
	if err != nil {
		t.Fatalf("chainParamsFor(flag): %v", err)
	}
	if got["reviewer"] != "flag-reviewer" {
		t.Errorf("flag tier: reviewer = %q, want flag-reviewer (the flag must beat the repo)", got["reviewer"])
	}
	if got["planner"] != "repo-planner" {
		t.Errorf("flag tier: planner = %q, want repo-planner (an ungiven flag leaves the repo standing)", got["planner"])
	}

	// An old flag beats the policy's own value.
	got, _, err = chainParamsFor(def, pol, ChainOptions{ReviewerActor: "flag-reviewer"}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(flag): %v", err)
	}
	if got["reviewer"] != "flag-reviewer" {
		t.Errorf("flag tier: reviewer = %q, want flag-reviewer (the flag must beat the policy)", got["reviewer"])
	}

	// --param beats the old flag.
	got, _, err = chainParamsFor(def, pol, ChainOptions{
		ReviewerActor: "flag-reviewer",
		Params:        map[string]string{"reviewer": "param-reviewer"},
	}, repo)
	if err != nil {
		t.Fatalf("chainParamsFor(param): %v", err)
	}
	if got["reviewer"] != "param-reviewer" {
		t.Errorf("param tier: reviewer = %q, want param-reviewer (--param must beat the flag)", got["reviewer"])
	}

	// The shipped direct-fix workflow resolves every tier to the same values
	// as the default: funding follows the shipped set, not the name.
	directFix := directFixDef(t)
	fundCases := []struct {
		opts ChainOptions
		repo RepoSettings
	}{
		{ChainOptions{}, RepoSettings{}},
		{ChainOptions{}, repo},
		{ChainOptions{ReviewerActor: "flag-reviewer"}, repo},
		{ChainOptions{ReviewerActor: "flag-reviewer", Params: map[string]string{"reviewer": "param-reviewer"}}, repo},
	}
	for _, tc := range fundCases {
		want, _, err := chainParamsFor(def, pol, tc.opts, tc.repo)
		if err != nil {
			t.Fatalf("chainParamsFor(default %+v): %v", tc.opts, err)
		}
		funded, _, err := chainParamsFor(directFix, pol, tc.opts, tc.repo)
		if err != nil {
			t.Fatalf("chainParamsFor(direct-fix %+v): %v", tc.opts, err)
		}
		if !reflect.DeepEqual(funded, want) {
			t.Errorf("chainParamsFor(direct-fix %+v) = %v, want the default's %v", tc.opts, funded, want)
		}
	}
}

// TestChainParamsFlagWithoutSlotListsParams pins the refusal an old flag earns
// when the chosen workflow has no slot for it: the error names the flag, the
// missing param and every param the workflow does take.
func TestChainParamsFlagWithoutSlotListsParams(t *testing.T) {
	def := workflow.Definition{
		Name:   "triage",
		Params: map[string]workflow.Param{"answer": {Kind: workflow.ParamBool, Bool: true}},
	}
	_, _, err := chainParamsFor(def, policy.Policy{}, ChainOptions{ReviewerActor: "assistant"}, RepoSettings{})
	if err == nil {
		t.Fatal("chainParamsFor(no slot) = nil error, want a refusal")
	}
	for _, want := range []string{"--reviewer-actor", "reviewer", "answer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestChainParamsNoGateRendersEmpty pins --no-gate: the gate param resolves to
// the empty string, so the check step renders no command, on the default and
// on direct-fix alike.
func TestChainParamsNoGateRendersEmpty(t *testing.T) {
	for _, def := range []workflow.Definition{workflow.Default(), directFixDef(t)} {
		got, _, err := chainParamsFor(def, policy.Policy{}, ChainOptions{NoGate: true}, RepoSettings{})
		if err != nil {
			t.Fatalf("chainParamsFor(%s --no-gate): %v", def.Name, err)
		}
		if got["gate"] != "" {
			t.Fatalf("%s gate = %q, want empty", def.Name, got["gate"])
		}
		applied, err := workflow.WithParams(def, got)
		if err != nil {
			t.Fatalf("WithParams(%s): %v", def.Name, err)
		}
		if rendered := workflow.RenderParams(applied, "{{params.gate}}"); rendered != "" {
			t.Errorf("%s {{params.gate}} renders %q, want empty", def.Name, rendered)
		}
	}
}

// TestChainParamsMatchChainSettings pins that chainParamsFor reproduces the
// settings the old engine resolves from the same flags and repository entry,
// for the flag combinations the start tests cover. The default workflow's
// params are the settings' own slots, so the two must agree flag for flag;
// the builder param must agree with the legacy builder member's actor, which
// resolves the same flag, then the entry, then the builder default.
func TestChainParamsMatchChainSettings(t *testing.T) {
	rt, _ := chainRuntime(t)
	defs := []workflow.Definition{workflow.Default(), directFixDef(t)}
	repoActors := RepoSettings{BuilderActor: "repo-builder", ReviewerActor: "repo-reviewer", PlannerActor: "repo-planner"}
	cases := []struct {
		opts ChainOptions
		repo RepoSettings
	}{
		{opts: ChainOptions{}},
		{opts: ChainOptions{Security: ptr(true)}},
		{opts: ChainOptions{Security: ptr(false)}},
		{opts: ChainOptions{ReviewerActor: "assistant"}},
		{opts: ChainOptions{PlannerActor: "lite-planner", SecurityActor: "security"}},
		{opts: ChainOptions{MaxCorrections: ptr(1)}},
		{opts: ChainOptions{Gate: "go test ./..."}},
		{opts: ChainOptions{NoGate: true}},
		{opts: ChainOptions{Regate: ptr(5)}},
		{opts: ChainOptions{}, repo: repoActors},
		{opts: ChainOptions{ReviewerActor: "flag-reviewer", PlannerActor: "flag-planner", BuilderActor: "flag-builder"}, repo: repoActors},
	}
	for _, def := range defs {
		for _, tc := range cases {
			opts, repo := tc.opts, tc.repo
			values, _, err := chainParamsFor(def, rt.Policy, opts, repo)
			if err != nil {
				t.Fatalf("%s chainParamsFor(%+v): %v", def.Name, opts, err)
			}
			applied, err := workflow.WithParams(def, values)
			if err != nil {
				t.Fatalf("%s WithParams(%+v): %v", def.Name, opts, err)
			}
			set := chainSettings(rt.Policy, opts, resolve.RoleChecks(rt.RoleRegistry(), "builder"), repo)
			render := func(param string) string {
				return workflow.RenderParams(applied, "{{params."+param+"}}")
			}
			checks := map[string]string{
				"reviewer":        set.ReviewerActor,
				"planner":         set.PlannerActor,
				"security":        set.SecurityActor,
				"gate":            set.Gate,
				"regate":          strconv.Itoa(set.Regate),
				"max_corrections": strconv.Itoa(set.MaxCorrections),
				"scan":            strconv.FormatBool(set.Security),
			}
			for param, want := range checks {
				if got := render(param); got != want {
					t.Errorf("%s %+v repo %+v: param %s = %q, want the settings' %q", def.Name, opts, repo, param, got, want)
				}
			}
			builder := chainMembersFor(opts, set, repo)[0]
			if builder.part != chain.MemberBuilder {
				t.Fatalf("%s %+v repo %+v: first legacy member is part %q, want the builder", def.Name, opts, repo, builder.part)
			}
			if got := render("builder"); got != builder.actor {
				t.Errorf("%s %+v repo %+v: param builder = %q, want the legacy member's %q", def.Name, opts, repo, got, builder.actor)
			}
		}
	}
}

// TestCustomWorkflowParamDefaultsBeatPolicy pins the MasterMind decision: policy
// fills a param only for the shipped default workflow, so a custom workflow's own
// default stands and a custom reviewer is never overwritten.
func TestCustomWorkflowParamDefaultsBeatPolicy(t *testing.T) {
	def := workflow.Definition{
		Name:   "triage",
		Params: map[string]workflow.Param{"reviewer": {Kind: workflow.ParamString, Str: "custom-reviewer"}},
	}
	pol := policy.Policy{Chain: &policy.ChainPolicy{ReviewerActor: "policy-reviewer"}}

	got, _, err := chainParamsFor(def, pol, ChainOptions{}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(custom): %v", err)
	}
	if _, ok := got["reviewer"]; ok {
		t.Errorf("chainParamsFor wrote reviewer = %q for a custom workflow, want policy to leave it alone", got["reviewer"])
	}

	// The repository entry fills nothing outside the shipped default either.
	got, _, err = chainParamsFor(def, pol, ChainOptions{}, RepoSettings{BuilderActor: "repo-builder", ReviewerActor: "repo-reviewer"})
	if err != nil {
		t.Fatalf("chainParamsFor(custom repo): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("chainParamsFor wrote %v for a custom workflow, want the repo entry to leave it alone", got)
	}
	applied, err := workflow.WithParams(def, got)
	if err != nil {
		t.Fatalf("WithParams: %v", err)
	}
	if r := workflow.RenderParams(applied, "{{params.reviewer}}"); r != "custom-reviewer" {
		t.Errorf("reviewer = %q, want the custom default", r)
	}
}

// TestChainRepoSuppliedActors pins which parts the repository entry supplies:
// an entry actor with no flag override, and only when the definition carries
// that value -- a --param override or a custom workflow leaves the map empty,
// so the refusal never blames the entry for an actor it did not supply.
func TestChainRepoSuppliedActors(t *testing.T) {
	t.Parallel()

	repo := RepoSettings{BuilderActor: "repo-builder", ReviewerActor: "repo-reviewer", PlannerActor: "repo-planner"}
	apply := func(def workflow.Definition, opts ChainOptions) workflow.Definition {
		t.Helper()
		values, _, err := chainParamsFor(def, policy.Policy{}, opts, repo)
		if err != nil {
			t.Fatalf("chainParamsFor: %v", err)
		}
		applied, err := workflow.WithParams(def, values)
		if err != nil {
			t.Fatalf("WithParams: %v", err)
		}
		return applied
	}

	def := workflow.Default()
	if got := chainRepoSuppliedActors(apply(def, ChainOptions{}), ChainOptions{}, repo); len(got) != 3 {
		t.Errorf("supplied = %v, want all three parts", got)
	}
	if got := chainRepoSuppliedActors(apply(def, ChainOptions{ReviewerActor: "flag-reviewer"}), ChainOptions{ReviewerActor: "flag-reviewer"}, repo); len(got) != 2 || got["reviewer"] != "" {
		t.Errorf("supplied with a reviewer flag = %v, want builder and planner only", got)
	}
	// A flag repeating the entry's own word still wins the tier: the entry
	// did not supply the actor, so the refusal must not blame it.
	same := ChainOptions{ReviewerActor: "repo-reviewer"}
	if got := chainRepoSuppliedActors(apply(def, same), same, repo); len(got) != 2 || got["reviewer"] != "" {
		t.Errorf("supplied with a flag repeating the entry = %v, want builder and planner only", got)
	}
	// A --param repeating the entry's own word still wins the tier: the
	// entry did not supply the actor, so the refusal must not blame it.
	sameParam := ChainOptions{Params: map[string]string{"reviewer": "repo-reviewer"}}
	if got := chainRepoSuppliedActors(apply(def, sameParam), sameParam, repo); len(got) != 2 || got["reviewer"] != "" {
		t.Errorf("supplied with a --param repeating the entry = %v, want builder and planner only", got)
	}
	if got := chainRepoSuppliedActors(apply(def, ChainOptions{Params: map[string]string{"planner": "param-planner"}}), ChainOptions{Params: map[string]string{"planner": "param-planner"}}, repo); len(got) != 2 || got["planner"] != "" {
		t.Errorf("supplied with a planner --param = %v, want builder and reviewer only", got)
	}

	custom := workflow.Definition{
		Name:   "triage",
		Params: map[string]workflow.Param{"reviewer": {Kind: workflow.ParamString, Str: "custom-reviewer"}},
	}
	if got := chainRepoSuppliedActors(custom, ChainOptions{}, repo); len(got) != 0 {
		t.Errorf("supplied for a custom workflow = %v, want empty", got)
	}
}

// TestDefaultWorkflowParamsComeFromPolicy pins the other half: the shipped
// default workflow still takes its reviewer from policy.
func TestDefaultWorkflowParamsComeFromPolicy(t *testing.T) {
	pol := policy.Policy{Chain: &policy.ChainPolicy{ReviewerActor: "policy-reviewer"}}

	got, _, err := chainParamsFor(workflow.Default(), pol, ChainOptions{}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(default): %v", err)
	}
	if got["reviewer"] != "policy-reviewer" {
		t.Errorf("reviewer = %q, want policy's policy-reviewer for the shipped default", got["reviewer"])
	}
}

// TestChainCleanupFillsRoundChainOrBoth pins the cleanup mapping: the flag
// beats the repository entry, an empty resolution fills neither slot, and the
// after word picks the round slot, the chain slot, or both. Filling only the
// round slot for both is the mutation this test catches.
func TestChainCleanupFillsRoundChainOrBoth(t *testing.T) {
	def := workflow.Default()
	cases := []struct {
		name             string
		opts             ChainOptions
		repo             RepoSettings
		round, chainSlot string
	}{
		{"the flag beats the repo",
			ChainOptions{Cleanup: "flag-clean", CleanupAfter: "round"},
			RepoSettings{Cleanup: "repo-clean", CleanupAfter: "chain"},
			"flag-clean", ""},
		{"the repo fills the chain slot",
			ChainOptions{},
			RepoSettings{Cleanup: "repo-clean", CleanupAfter: "chain"},
			"", "repo-clean"},
		{"round fills only the round slot",
			ChainOptions{Cleanup: "flag-clean", CleanupAfter: "round"},
			RepoSettings{},
			"flag-clean", ""},
		{"both fills both slots",
			ChainOptions{Cleanup: "flag-clean", CleanupAfter: "both"},
			RepoSettings{},
			"flag-clean", "flag-clean"},
		{"the repo after both fills both slots",
			ChainOptions{},
			RepoSettings{Cleanup: "repo-clean", CleanupAfter: "both"},
			"repo-clean", "repo-clean"},
		{"empty fills neither",
			ChainOptions{},
			RepoSettings{},
			"", ""},
	}
	for _, tc := range cases {
		got, _, err := chainParamsFor(def, policy.Policy{}, tc.opts, tc.repo)
		if err != nil {
			t.Fatalf("%s: chainParamsFor: %v", tc.name, err)
		}
		if got["cleanup_round"] != tc.round || got["cleanup_chain"] != tc.chainSlot {
			t.Errorf("%s: round, chain = %q, %q, want %q, %q",
				tc.name, got["cleanup_round"], got["cleanup_chain"], tc.round, tc.chainSlot)
		}
	}
}

// TestChainCleanupNoCleanupClears pins --no-cleanup: a repository entry's
// cleanup is cleared, so neither slot is filled. Keeping the entry's command
// is the mutation this test catches.
func TestChainCleanupNoCleanupClears(t *testing.T) {
	got, _, err := chainParamsFor(workflow.Default(), policy.Policy{},
		ChainOptions{NoCleanup: true}, RepoSettings{Cleanup: "repo-clean", CleanupAfter: "both"})
	if err != nil {
		t.Fatalf("chainParamsFor(--no-cleanup): %v", err)
	}
	if got["cleanup_round"] != "" || got["cleanup_chain"] != "" {
		t.Errorf("round, chain = %q, %q, want neither filled",
			got["cleanup_round"], got["cleanup_chain"])
	}
}

// TestChainCleanupParamBeatsTheFlag pins that --param still wins per-param: a
// cleanup_round override replaces the flag's command in that slot while the
// other slot keeps it. Letting the flag win is the mutation this test
// catches.
func TestChainCleanupParamBeatsTheFlag(t *testing.T) {
	got, _, err := chainParamsFor(workflow.Default(), policy.Policy{},
		ChainOptions{Cleanup: "flag-clean", CleanupAfter: "both",
			Params: map[string]string{"cleanup_round": "param-clean"}},
		RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(--param): %v", err)
	}
	if got["cleanup_round"] != "param-clean" {
		t.Errorf("cleanup_round = %q, want param-clean (--param must beat the flag)", got["cleanup_round"])
	}
	if got["cleanup_chain"] != "flag-clean" {
		t.Errorf("cleanup_chain = %q, want flag-clean (an unoverridden slot keeps the flag)", got["cleanup_chain"])
	}
}

// TestChainCleanupBadAfterRefuses pins the refusal a bad after word earns,
// whether it came from the flag or the repository entry.
func TestChainCleanupBadAfterRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts ChainOptions
		repo RepoSettings
	}{
		{"a bad flag after", ChainOptions{Cleanup: "make clean", CleanupAfter: "weekly"}, RepoSettings{}},
		{"a bad repo after", ChainOptions{}, RepoSettings{Cleanup: "make clean", CleanupAfter: "weekly"}},
	} {
		_, _, err := chainParamsFor(workflow.Default(), policy.Policy{}, tc.opts, tc.repo)
		if err == nil {
			t.Errorf("%s: chainParamsFor = nil error, want a refusal", tc.name)
			continue
		}
		for _, want := range []string{`cleanup_after is "weekly"`, "round, chain or both"} {
			if got := err.Error(); !strings.Contains(got, want) {
				t.Errorf("%s: error = %q, want it to name %q", tc.name, got, want)
			}
		}
	}
}

// TestChainCleanupFlagWithoutSlotRefuses pins the refusal a --cleanup earns
// when the chosen workflow has no slot for it: the error names the flag, the
// missing param and every param the workflow does take.
func TestChainCleanupFlagWithoutSlotRefuses(t *testing.T) {
	def := workflow.Definition{
		Name:   "triage",
		Params: map[string]workflow.Param{"answer": {Kind: workflow.ParamBool, Bool: true}},
	}
	_, _, err := chainParamsFor(def, policy.Policy{}, ChainOptions{Cleanup: "make clean"}, RepoSettings{})
	if err == nil {
		t.Fatal("chainParamsFor(no slot) = nil error, want a refusal")
	}
	for _, want := range []string{"--cleanup", "cleanup_chain", "answer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}
