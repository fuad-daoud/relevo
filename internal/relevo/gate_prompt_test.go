package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// promptForRound loads name's binding under the store lock and returns its
// composed handoff prompt: the wrapper every round starter hands the runner.
func promptForRound(t *testing.T, rt Runtime, name string) string {
	t.Helper()
	var got string
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		got = roundPrompt(rt, tx, b, "p", "r", "d")
		return nil
	})
	if err != nil {
		t.Fatalf("compose the %s prompt: %v", name, err)
	}
	return got
}

// TestLoneSendPromptNamesTheBoundGate pins the lone path end to end: a bind
// --gate + send hands the process a prompt naming the gate relevo runs after
// the round.
func TestLoneSendPromptNamesTheBoundGate(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
		Gate: "make check",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	specs := runnerOf(t, rt).specs
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want one Start", len(specs))
	}
	want := gateLineFor("make check")
	for _, arg := range specs[0].Argv {
		if strings.Contains(arg, want) {
			return
		}
	}
	t.Errorf("sent prompt does not carry %q, argv = %q", want, specs[0].Argv)
}

// TestChainPlanRoundPromptNamesTheStoredCheck pins the local chain's plan
// round: the writer carries no gate of its own, so the line names the stored
// workflow's one check command.
func TestChainPlanRoundPromptNamesTheStoredCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowGateParamWorkflow)

	if got := chainBinding(t, rt, "shop").Gate; got != "" {
		t.Fatalf("local writer gate = %q, want none: the line must come from the stored definition", got)
	}
	want := gateLineFor("make check")
	if got := promptForRound(t, rt, "shop"); !strings.Contains(got, want) {
		t.Errorf("plan-round prompt does not carry %q:\n%s", want, got)
	}
}

// TestServedWorkflowChainPlanRoundPromptNamesTheStoredCheck pins the
// --server workflow path's plan round: the writer's gate is empty there too,
// so the line still names the stored workflow's one check command.
func TestServedWorkflowChainPlanRoundPromptNamesTheStoredCheck(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := servedChainRequest("shop")
	req.Workflow = []byte(flowGateParamWorkflow)
	servedChainStart(t, rt, req)

	if got := chainBinding(t, rt, "shop").Gate; got != "" {
		t.Fatalf("served writer gate = %q, want none: the line must come from the stored definition", got)
	}
	want := gateLineFor("make check")
	if got := promptForRound(t, rt, "shop"); !strings.Contains(got, want) {
		t.Errorf("served plan-round prompt does not carry %q:\n%s", want, got)
	}
}

// TestChainWithoutGatePromptNamesNoCheck pins the empty workflow: a chain
// whose checks render no command carries no gate line.
func TestChainWithoutGatePromptNamesNoCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowNoGateWorkflow)

	if got := promptForRound(t, rt, "shop"); strings.Contains(got, "relevo runs `") {
		t.Errorf("a checkless chain's prompt must carry no gate line, got:\n%s", got)
	}
}

// TestChainBuildFixRoundPromptNamesTheStoredCheck pins the correction round:
// after a changes verdict and a correction plan, the builder's build-fix
// round carries the same line as its plan round.
func TestChainBuildFixRoundPromptNamesTheStoredCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Gate: "make check", MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainGreenCheck(t, rt, "shop")
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainPlanBody())

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want the build-fix round 2", builder.Round)
	}
	if builder.Gate != "" {
		t.Fatalf("build-fix writer gate = %q, want none: the line must come from the stored definition", builder.Gate)
	}
	want := gateLineFor("make check")
	if got := promptForRound(t, rt, "shop"); !strings.Contains(got, want) {
		t.Errorf("build-fix prompt does not carry %q:\n%s", want, got)
	}
}

// TestChainRepairRoundPromptNamesTheStoredCheck pins the repair round: after
// a red check within budget, the builder's repair round carries the same line
// as its plan round.
func TestChainRepairRoundPromptNamesTheStoredCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Gate: "make check", Regate: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL the same thing\n")

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want the repair round 2", builder.Round)
	}
	if builder.Gate != "" {
		t.Fatalf("repair writer gate = %q, want none: the line must come from the stored definition", builder.Gate)
	}
	want := gateLineFor("make check")
	if got := promptForRound(t, rt, "shop"); !strings.Contains(got, want) {
		t.Errorf("repair-round prompt does not carry %q:\n%s", want, got)
	}
}
