package relevo

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestServerChainParamRefusedWithoutWorkflowFeature pins the start gate: a
// --param the legacy settings-only wire cannot carry is refused with the
// workflow-feature wording before any create, branch or row.
func TestServerChainParamRefusedWithoutWorkflowFeature(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, fg, _ := chainServerRuntime(t, fr)

	opts := chainServerOpts(t)
	opts.Params = map[string]string{"gate": "make custom-check"}

	_, err := ChainStart(context.Background(), rt, opts)
	if err == nil {
		t.Fatal("ChainStart = nil, want refusal for --param without the workflow feature")
	}
	if !strings.Contains(err.Error(), "--param") || !strings.Contains(err.Error(), "server's workflow feature is not available yet") {
		t.Fatalf("err = %q, want the --param workflow-feature refusal", err.Error())
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateChain") {
			t.Fatalf("CreateChain called: %v, want it refused beforehand", fr.calls)
		}
	}
	assertNothingCreated(t, rt, fg, "shop")
}

// TestServerChainParamFlowsWithWorkflowFeature pins the consistent rule's
// other half: with the feature the param flows through the existing
// chainParamsFor/WithParams path into the shipped Workflow JSON.
func TestServerChainParamFlowsWithWorkflowFeature(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureWorkflow)
	rt, _, ft := chainServerRuntime(t, fr)
	ft.snapshotResp = remote.Snapshot{
		Heads: map[string]string{"refs/relevo/shop/out": seedRemoteBase},
		Body:  io.NopCloser(strings.NewReader("bundle-bytes")),
	}

	opts := chainServerOpts(t)
	opts.Params = map[string]string{"gate": "make custom-check"}

	res, err := ChainStart(context.Background(), rt, opts)
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}

	req := fr.createChainReq
	if len(req.Workflow) == 0 {
		t.Fatal("create Workflow is empty, want the parameterized definition")
	}
	def, err := workflow.Parse(req.Workflow)
	if err != nil {
		t.Fatalf("parse Workflow: %v", err)
	}
	if got := def.Params["gate"].Str; got != "make custom-check" {
		t.Fatalf("Workflow gate param = %q, want the --param value", got)
	}
	if res.Chain.Name != "shop" {
		t.Errorf("Chain.Name = %q, want shop", res.Chain.Name)
	}
}

// haltServedChainAtReview parks a served chain halted on its review step with
// the build done, so a resume re-enters the reviewer while the builder stands
// by swappable.
func haltServedChainAtReview(t *testing.T, rt Runtime, name string) {
	t.Helper()
	builder := chainBinding(t, rt, name)
	builder.Round = 2
	if err := rt.Store.Save(builder); err != nil {
		t.Fatalf("advance the builder: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain(name)
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusHalted)
		var st workflow.State
		if err := json.Unmarshal(row.StateJSON, &st); err != nil {
			return err
		}
		st.Status = workflow.StatusHalted
		st.At = "review"
		st.Awaiting.Step = "review"
		st.Awaiting.Member = "reviewer"
		st.Awaiting.Round = 1
		stateJSON, err := json.Marshal(st)
		if err != nil {
			return err
		}
		row.StateJSON = stateJSON
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("halt the chain: %v", err)
	}
}

// TestServedChainResumeParamSwapsTheBuilder pins that --param builder=<actor>
// on a resume still swaps the served member: the param reaches the stored
// definition, the builder member's actor changes, and the server re-picks
// from the new actor's list.
func TestServedChainResumeParamSwapsTheBuilder(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	const altCandidate = "claude/test/alt-model"
	rt.Candidates = candidateSet(t, `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"alt-model","roles":["builder"]}
]`)
	rows := chainRows()
	rows["alt-builder"] = roles.Row{
		Shape:       ptr("writer"),
		Candidates:  []string{altCandidate},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "alt-builder"}},
	}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)

	_ = servedChainStart(t, rt, servedChainRequest("shop"))
	haltServedChainAtReview(t, rt, "shop")

	res, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:   "shop",
		Params: map[string]string{"builder": "alt-builder"},
	})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if res.Chain.Status != string(chain.StatusRunning) {
		t.Errorf("result chain status = %q, want running", res.Chain.Status)
	}

	memAfter := chainBinding(t, rt, "shop")
	if memAfter.Role != "alt-builder" {
		t.Errorf("role = %q, want alt-builder", memAfter.Role)
	}
	if memAfter.BuilderCandidate != altCandidate {
		t.Errorf("candidate = %q, want %q", memAfter.BuilderCandidate, altCandidate)
	}
	if memAfter.Builder.Kind != "claude" {
		t.Errorf("Builder.Kind = %q, want claude", memAfter.Builder.Kind)
	}

	members, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers after: %v", err)
	}
	found := false
	for _, m := range members {
		if m.Binding == "shop" {
			found = true
			if m.Actor != "alt-builder" {
				t.Errorf("chain_member Actor = %q, want alt-builder", m.Actor)
			}
		}
	}
	if !found {
		t.Error("shop not found in chain_members")
	}
}

// TestServedChainResumeParamKeepsOtherMembers pins the swap's scope: only the
// builder member moves, and a failed swap leaves the chain halted.
func TestServedChainResumeParamKeepsOtherMembers(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	const altCandidate = "claude/test/alt-model"
	rt.Candidates = candidateSet(t, `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"alt-model","roles":["builder"]}
]`)
	rows := chainRows()
	rows["alt-builder"] = roles.Row{
		Shape:       ptr("writer"),
		Candidates:  []string{altCandidate},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "alt-builder"}},
	}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)

	_ = servedChainStart(t, rt, servedChainRequest("shop"))
	revBefore := chainBinding(t, rt, "shop-rev")
	haltServedChainAtReview(t, rt, "shop")

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:   "shop",
		Params: map[string]string{"builder": "alt-builder"},
	}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	revAfter := chainBinding(t, rt, "shop-rev")
	if revAfter.BuilderCandidate != revBefore.BuilderCandidate {
		t.Errorf("reviewer candidate = %q, want untouched %q", revAfter.BuilderCandidate, revBefore.BuilderCandidate)
	}
	if revAfter.Role != revBefore.Role {
		t.Errorf("reviewer role = %q, want untouched %q", revAfter.Role, revBefore.Role)
	}
	if _, err := rt.Store.Chain("shop"); err != nil {
		t.Fatalf("chain row: %v", err)
	}
}
