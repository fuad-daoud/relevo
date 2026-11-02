package relevo

import (
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

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
	got, err := chainParamsFor(def, pol, ChainOptions{}, RepoSettings{})
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
	got, err = chainParamsFor(def, pol, ChainOptions{}, repo)
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
	got, err = chainParamsFor(def, pol, ChainOptions{ReviewerActor: "flag-reviewer"}, repo)
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
	got, err = chainParamsFor(def, pol, ChainOptions{ReviewerActor: "flag-reviewer"}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(flag): %v", err)
	}
	if got["reviewer"] != "flag-reviewer" {
		t.Errorf("flag tier: reviewer = %q, want flag-reviewer (the flag must beat the policy)", got["reviewer"])
	}

	// --param beats the old flag.
	got, err = chainParamsFor(def, pol, ChainOptions{
		ReviewerActor: "flag-reviewer",
		Params:        map[string]string{"reviewer": "param-reviewer"},
	}, repo)
	if err != nil {
		t.Fatalf("chainParamsFor(param): %v", err)
	}
	if got["reviewer"] != "param-reviewer" {
		t.Errorf("param tier: reviewer = %q, want param-reviewer (--param must beat the flag)", got["reviewer"])
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
	_, err := chainParamsFor(def, policy.Policy{}, ChainOptions{ReviewerActor: "assistant"}, RepoSettings{})
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
// the empty string, so the check step renders no command.
func TestChainParamsNoGateRendersEmpty(t *testing.T) {
	got, err := chainParamsFor(workflow.Default(), policy.Policy{}, ChainOptions{NoGate: true}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(--no-gate): %v", err)
	}
	if got["gate"] != "" {
		t.Fatalf("gate = %q, want empty", got["gate"])
	}
	applied, err := workflow.WithParams(workflow.Default(), got)
	if err != nil {
		t.Fatalf("WithParams: %v", err)
	}
	if rendered := workflow.RenderParams(applied, "{{params.gate}}"); rendered != "" {
		t.Errorf("{{params.gate}} renders %q, want empty", rendered)
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
	def := workflow.Default()
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
	for _, tc := range cases {
		opts, repo := tc.opts, tc.repo
		values, err := chainParamsFor(def, rt.Policy, opts, repo)
		if err != nil {
			t.Fatalf("chainParamsFor(%+v): %v", opts, err)
		}
		applied, err := workflow.WithParams(def, values)
		if err != nil {
			t.Fatalf("WithParams(%+v): %v", opts, err)
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
				t.Errorf("%+v repo %+v: param %s = %q, want the settings' %q", opts, repo, param, got, want)
			}
		}
		builder := chainMembersFor(opts, set, repo)[0]
		if builder.part != chain.MemberBuilder {
			t.Fatalf("%+v repo %+v: first legacy member is part %q, want the builder", opts, repo, builder.part)
		}
		if got := render("builder"); got != builder.actor {
			t.Errorf("%+v repo %+v: param builder = %q, want the legacy member's %q", opts, repo, got, builder.actor)
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

	got, err := chainParamsFor(def, pol, ChainOptions{}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(custom): %v", err)
	}
	if _, ok := got["reviewer"]; ok {
		t.Errorf("chainParamsFor wrote reviewer = %q for a custom workflow, want policy to leave it alone", got["reviewer"])
	}

	// The repository entry fills nothing outside the shipped default either.
	got, err = chainParamsFor(def, pol, ChainOptions{}, RepoSettings{BuilderActor: "repo-builder", ReviewerActor: "repo-reviewer"})
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
		values, err := chainParamsFor(def, policy.Policy{}, opts, repo)
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

	got, err := chainParamsFor(workflow.Default(), pol, ChainOptions{}, RepoSettings{})
	if err != nil {
		t.Fatalf("chainParamsFor(default): %v", err)
	}
	if got["reviewer"] != "policy-reviewer" {
		t.Errorf("reviewer = %q, want policy's policy-reviewer for the shipped default", got["reviewer"])
	}
}
