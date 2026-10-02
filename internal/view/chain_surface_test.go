package view

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRenderStatusChainRow is the human surface: a row carrying chain facts
// renders the chain line and its reason instead of the binding block, while a
// member row beside it keeps the block it always had.
func TestRenderStatusChainRow(t *testing.T) {
	t.Parallel()

	chain := BindingStatus{
		Name: "x", CWD: "/repo/x", State: string(store.StateNeedsYou), Display: "NEEDS YOU",
		Detail: "builder halted on plan 2: check red",
		Chain: &ChainFacts{
			Status: "halted", Phase: "build", Step: "reviewing",
			Plan: 2, Plans: 4, Corrections: 1, Awaiting: "builder",
			Reason: "builder halted on plan 2: check red",
		},
	}

	out := RenderStatus(Report{Bindings: []BindingStatus{chain}})
	if !strings.Contains(out, "chain  halted  plan 2/4 · reviewing · 1 correction") {
		t.Errorf("chain line missing from:\n%s", out)
	}
	if !strings.Contains(out, "  reason   builder halted on plan 2: check red") {
		t.Errorf("halt reason line missing from:\n%s", out)
	}
	// A chain row is the chain line, not a binding block.
	if strings.Contains(out, "  runner  ") || strings.Contains(out, "MasterMind") {
		t.Errorf("a chain row must not render a binding block:\n%s", out)
	}

	// A member row beside it keeps the block it always had.
	mixed := RenderStatus(Report{Bindings: []BindingStatus{
		chain,
		{Name: "plain", CWD: "/repo/plain", Round: 1, Display: "ACTIVE"},
	}})
	if !strings.Contains(mixed, "plain") || !strings.Contains(mixed, "  runner  ") {
		t.Errorf("the ordinary row must keep its block:\n%s", mixed)
	}
	if !strings.Contains(mixed, "chain  halted  plan 2/4 · reviewing · 1 correction") {
		t.Errorf("the chain row must still render beside a member row:\n%s", mixed)
	}
}

// TestRenderStatusChainRowWithoutCWD pins the empty-cwd fallback: a chain with
// no worktree recorded prints "-" in the column a binding's cwd sits in.
func TestRenderStatusChainRowWithoutCWD(t *testing.T) {
	t.Parallel()

	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "x", Display: "ACTIVE",
		Chain: &ChainFacts{Status: "running", Phase: "build", Step: "building", Plan: 1, Plans: 1},
	}}})

	if !strings.Contains(out, "x        - ") {
		t.Errorf("missing cwd fallback in:\n%s", out)
	}
	if strings.Contains(out, "reason") {
		t.Errorf("a running chain carries no reason line:\n%s", out)
	}
}

// TestStatuslineChainRowShowsThePlanSegment is the statusline surface: the
// chain row's middle is the chain text, not the round-and-actor segment, and
// its status column follows the chain.
func TestStatuslineChainRowShowsThePlanSegment(t *testing.T) {
	t.Parallel()

	rep := Report{Bindings: []BindingStatus{{
		Name:    "x",
		Display: "ACTIVE",
		Chain: &ChainFacts{
			Status: "running", Phase: "build", Step: "reviewing",
			Plan: 2, Plans: 4, Corrections: 1, Awaiting: "reviewer",
		},
	}}}

	rows := StatusLineRows(rep, baseTime)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	want := "chain x · plan 2/4 · reviewing · 1 correction"
	if rows[0].Chain != want {
		t.Errorf("Chain = %q, want %q", rows[0].Chain, want)
	}
	if rows[0].NeedsYou || rows[0].Status != "ACTIVE" || rows[0].Tone != "phase" {
		t.Errorf("running chain row = needs %v, status %q, tone %q; want false/ACTIVE/phase",
			rows[0].NeedsYou, rows[0].Status, rows[0].Tone)
	}

	plain := stripSGR(splitLines(RenderStatusLine(rep, baseTime, 120))[0])
	if !strings.Contains(plain, want) {
		t.Errorf("rendered line %q does not carry the chain segment %q", plain, want)
	}
	if strings.Contains(plain, "r0 · ") {
		t.Errorf("a chain row must not carry the round-and-actor middle: %q", plain)
	}

	// A chain that halted or was stopped reads NEEDS YOU in that column.
	rep.Bindings[0].Display = "NEEDS YOU"
	rep.Bindings[0].Chain.Status = "halted"
	rep.Bindings[0].Chain.Reason = "reviewer still wants changes after 1 corrections"
	rows = StatusLineRows(rep, baseTime)
	if !rows[0].NeedsYou || rows[0].Status != "NEEDS YOU" || rows[0].Tone != "needs" {
		t.Errorf("halted chain row = needs %v, status %q, tone %q; want true/NEEDS YOU/needs",
			rows[0].NeedsYou, rows[0].Status, rows[0].Tone)
	}

	// A settled chain reads DONE with quiet tone, drops the step from its segment, and has no round clock.
	rep.Bindings[0].Display = "DONE"
	rep.Bindings[0].Chain.Status = "done"
	rep.Bindings[0].Chain.Reason = ""
	rows = StatusLineRows(rep, baseTime)
	if rows[0].NeedsYou || rows[0].Status != "DONE" || rows[0].Tone != "quiet" {
		t.Errorf("done chain row = needs %v, status %q, tone %q; want false/DONE/quiet",
			rows[0].NeedsYou, rows[0].Status, rows[0].Tone)
	}
	wantDone := "chain x · plan 2/4 · 1 correction"
	if rows[0].Chain != wantDone {
		t.Errorf("Chain = %q, want %q", rows[0].Chain, wantDone)
	}
	plainDone := stripSGR(splitLines(RenderStatusLine(rep, baseTime, 120))[0])
	if !strings.Contains(plainDone, wantDone) {
		t.Errorf("rendered line %q does not carry the chain segment %q", plainDone, wantDone)
	}
	if !strings.Contains(plainDone, "DONE") {
		t.Errorf("rendered line %q does not show DONE: %q", plainDone, plainDone)
	}
	if strings.Contains(plainDone, "r0 · ") {
		t.Errorf("a done chain row must not carry the round-and-actor middle: %q", plainDone)
	}
}
