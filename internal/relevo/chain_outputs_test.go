package relevo

import (
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// flowSecurityWorkflow is the scan workflow: a plan is built and the security
// member counts findings, so a member close routes on its own count.
const flowSecurityWorkflow = `name: secflow
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: scan } }
  scan: { run: security, seed: "scan the build", on: { findings=0: done, findings>0: { halt: "findings" } } }
`

// TestChainParseOutcomesFencelessSecurityBlockDoesNotHalt pins the security
// member's closing block whose backtick fences were lost: the count still
// reaches the chain, so a clean scan closes the step done instead of halting on
// a block the round did write.
func TestChainParseOutcomesFencelessSecurityBlockDoesNotHalt(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowSecurityWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: reporttail.OutcomeDone})

	body := []byte("I scanned the branch.\n\nrelevo\nfindings: 0\n")
	path := writeGapReaderOutput(t, body)
	sec := chainBinding(t, rt, "shop-security")

	ev := closeWFEvent(t, rt, sec, body, path, reporttail.OutcomeUnstructured)
	if ev.Status != reporttail.OutcomeDone {
		t.Fatalf("event status = %q with reason %q, want done from the fenceless block", ev.Status, ev.Reason)
	}
	if ev.Outcomes["findings"] != "0" {
		t.Fatalf("outcomes = %+v, want findings=0 read from the fenceless block", ev.Outcomes)
	}
	flowAdvance(t, rt, ev)
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("row = %s %q, want done: a clean scan must not halt the chain", got.Status, got.Reason)
	}
}

func TestChainParseOutcomesBodyThenStream(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	rev := chainBinding(t, rt, "shop-rev")
	info, ok := rt.RoleRegistry().ActorInfo(BindingRole(rev))
	if !ok {
		t.Fatalf("no actor info for reviewer")
	}

	body := []byte("Review complete.\n\n```relevo\nverdict: pass\n```\n")
	vals, missing := chainParseOutcomes(rt, rev, rev.Round, body, info.Outputs)
	if missing != "" {
		t.Fatalf("expected no missing outcome, got %q", missing)
	}
	if vals["verdict"] != "pass" {
		t.Fatalf("got verdict %q, want pass", vals["verdict"])
	}

	streamText := "Stream review.\n\n```relevo\nverdict: changes\n```\n"
	streamData := chainStreamResultLine(t, streamText)
	if err := os.WriteFile(rt.Store.StreamPath("shop-rev", rev.Round), []byte(streamData), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	recapBody := []byte("Recap only, no block here.\n")
	vals, missing = chainParseOutcomes(rt, rev, rev.Round, recapBody, info.Outputs)
	if missing != "" {
		t.Fatalf("expected stream fallback to find verdict, got %q", missing)
	}
	if vals["verdict"] != "changes" {
		t.Fatalf("got verdict %q, want changes", vals["verdict"])
	}
}

func TestChainParseOutcomesRecapAfterBlock(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	rev := chainBinding(t, rt, "shop-rev")
	info, ok := rt.RoleRegistry().ActorInfo(BindingRole(rev))
	if !ok {
		t.Fatalf("no actor info for reviewer")
	}

	stream := chainStreamResultLine(t, "I reviewed the round.\n\n```relevo\nverdict: pass\n```\n") +
		chainStreamResultLine(t, chainRecapText)
	if err := os.WriteFile(rt.Store.StreamPath("shop-rev", rev.Round), []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}

	body := []byte(chainRecapText)
	vals, missing := chainParseOutcomes(rt, rev, rev.Round, body, info.Outputs)
	if missing != "" {
		t.Fatalf("expected stream scan newest-first to find verdict, got %q", missing)
	}
	if vals["verdict"] != "pass" {
		t.Fatalf("got verdict %q, want pass", vals["verdict"])
	}
}

func TestChainParseOutcomesMissingAndOutOfRange(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	rev := chainBinding(t, rt, "shop-rev")
	info, ok := rt.RoleRegistry().ActorInfo(BindingRole(rev))
	if !ok {
		t.Fatalf("no actor info for reviewer")
	}

	bodyNoBlock := []byte("no block here")
	_, missing := chainParseOutcomes(rt, rev, rev.Round, bodyNoBlock, info.Outputs)
	if missing == "" {
		t.Fatal("expected missing error, got none")
	}
	if !strings.Contains(missing, "verdict: no relevo block carries it") {
		t.Fatalf("unexpected missing reason: %q", missing)
	}

	bodyInvalidVerdict := []byte("```relevo\nverdict: maybe\n```\n")
	_, missing = chainParseOutcomes(rt, rev, rev.Round, bodyInvalidVerdict, info.Outputs)
	if missing == "" {
		t.Fatal("expected invalid outcome error, got none")
	}
	if !strings.Contains(missing, "verdict: \"maybe\" is not one of") {
		t.Fatalf("unexpected missing reason: %q", missing)
	}

	secOutputs := workflow.Outputs{
		"findings": workflow.Output{Kind: workflow.OutputCount},
	}
	bodyBadCount := []byte("```relevo\nfindings: not-a-number\n```\n")
	_, missing = chainParseOutcomes(rt, rev, rev.Round, bodyBadCount, secOutputs)
	if missing == "" {
		t.Fatal("expected invalid count error, got none")
	}
	if !strings.Contains(missing, "findings: \"not-a-number\" is not a count") {
		t.Fatalf("unexpected missing reason: %q", missing)
	}
}
