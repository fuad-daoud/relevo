package relevo

import (
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestChainParamsPrecedence pins the order chainParamsFor resolves a param in:
// the workflow default, then policy, then an old flag, then --param. A flag
// must beat the policy, and --param must beat the flag.
func TestChainParamsPrecedence(t *testing.T) {
	def := workflow.Default()
	pol := policy.Policy{Chain: &policy.ChainPolicy{ReviewerActor: "policy-reviewer"}}

	// Policy fills an unflagged slot.
	got, err := chainParamsFor(def, pol, ChainOptions{})
	if err != nil {
		t.Fatalf("chainParamsFor(policy): %v", err)
	}
	if got["reviewer"] != "policy-reviewer" {
		t.Errorf("policy tier: reviewer = %q, want policy-reviewer", got["reviewer"])
	}

	// An old flag beats the policy's own value.
	got, err = chainParamsFor(def, pol, ChainOptions{ReviewerActor: "flag-reviewer"})
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
	})
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
	_, err := chainParamsFor(def, policy.Policy{}, ChainOptions{ReviewerActor: "assistant"})
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
	got, err := chainParamsFor(workflow.Default(), policy.Policy{}, ChainOptions{NoGate: true})
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
// settings the old engine resolves from the same flags, for the flag
// combinations the start tests cover. The default workflow's params are the
// settings' own slots, so the two must agree flag for flag.
func TestChainParamsMatchChainSettings(t *testing.T) {
	rt, _ := chainRuntime(t)
	def := workflow.Default()
	cases := []ChainOptions{
		{},
		{Security: ptr(true)},
		{Security: ptr(false)},
		{ReviewerActor: "assistant"},
		{PlannerActor: "lite-planner", SecurityActor: "security"},
		{MaxCorrections: ptr(1)},
		{Gate: "go test ./..."},
		{NoGate: true},
		{Regate: ptr(5)},
	}
	for _, opts := range cases {
		values, err := chainParamsFor(def, rt.Policy, opts)
		if err != nil {
			t.Fatalf("chainParamsFor(%+v): %v", opts, err)
		}
		applied, err := workflow.WithParams(def, values)
		if err != nil {
			t.Fatalf("WithParams(%+v): %v", opts, err)
		}
		set := chainSettings(rt.Policy, opts, roleChecks(rt.RoleRegistry(), "builder"))
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
				t.Errorf("%+v: param %s = %q, want the settings' %q", opts, param, got, want)
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

	got, err := chainParamsFor(def, pol, ChainOptions{})
	if err != nil {
		t.Fatalf("chainParamsFor(custom): %v", err)
	}
	if _, ok := got["reviewer"]; ok {
		t.Errorf("chainParamsFor wrote reviewer = %q for a custom workflow, want policy to leave it alone", got["reviewer"])
	}
	applied, err := workflow.WithParams(def, got)
	if err != nil {
		t.Fatalf("WithParams: %v", err)
	}
	if r := workflow.RenderParams(applied, "{{params.reviewer}}"); r != "custom-reviewer" {
		t.Errorf("reviewer = %q, want the custom default", r)
	}
}

// TestDefaultWorkflowParamsComeFromPolicy pins the other half: the shipped
// default workflow still takes its reviewer from policy.
func TestDefaultWorkflowParamsComeFromPolicy(t *testing.T) {
	pol := policy.Policy{Chain: &policy.ChainPolicy{ReviewerActor: "policy-reviewer"}}

	got, err := chainParamsFor(workflow.Default(), pol, ChainOptions{})
	if err != nil {
		t.Fatalf("chainParamsFor(default): %v", err)
	}
	if got["reviewer"] != "policy-reviewer" {
		t.Errorf("reviewer = %q, want policy's policy-reviewer for the shipped default", got["reviewer"])
	}
}
