package relevo

import (
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

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
	vals, missing := chainParseOutcomes(rt, rev, body, info.Outputs)
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
	vals, missing = chainParseOutcomes(rt, rev, recapBody, info.Outputs)
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
	vals, missing := chainParseOutcomes(rt, rev, body, info.Outputs)
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
	_, missing := chainParseOutcomes(rt, rev, bodyNoBlock, info.Outputs)
	if missing == "" {
		t.Fatal("expected missing error, got none")
	}
	if !strings.Contains(missing, "verdict: no relevo block carries it") {
		t.Fatalf("unexpected missing reason: %q", missing)
	}

	bodyInvalidVerdict := []byte("```relevo\nverdict: maybe\n```\n")
	_, missing = chainParseOutcomes(rt, rev, bodyInvalidVerdict, info.Outputs)
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
	_, missing = chainParseOutcomes(rt, rev, bodyBadCount, secOutputs)
	if missing == "" {
		t.Fatal("expected invalid count error, got none")
	}
	if !strings.Contains(missing, "findings: \"not-a-number\" is not a count") {
		t.Fatalf("unexpected missing reason: %q", missing)
	}
}
