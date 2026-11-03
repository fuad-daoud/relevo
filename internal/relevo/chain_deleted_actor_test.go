package relevo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestChainDeletedActorHaltNamesTheResumeCommand pins the exact resume each
// part's halt names when the member's actor is gone from config actors. The
// reviewer, planner and security swaps have their own resume flags; the
// builder's own flag a resume refuses, so its halt names the builder param.
func TestChainDeletedActorHaltNamesTheResumeCommand(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		part, actor, command string
	}{
		{"builder", "builder", "relevo chain --resume --name shop --param builder=<other>"},
		{"reviewer", "reviewer", "relevo chain --resume --name shop --reviewer-actor <other>"},
		{"planner", "lite-planner", "relevo chain --resume --name shop --planner-actor <other>"},
		{"security", "security", "relevo chain --resume --name shop --security-actor <other>"},
	} {
		err := fmt.Errorf("binding %q builder: binding %s runs actor %q, which config actors no longer defines: %w",
			"shop", "shop", tc.actor, resolve.ErrUnknownRole)
		fallback := "builder spawn failed: " + err.Error()
		got := chainDeletedActorHalt(tc.part, "shop", tc.actor, err, fallback)
		if !strings.Contains(got, tc.command) {
			t.Errorf("part %s halt = %q, want it to name %q", tc.part, got, tc.command)
		}
		if !strings.Contains(got, fmt.Sprintf("actor %q was removed after chain %q started", tc.actor, "shop")) {
			t.Errorf("part %s halt = %q, want it to say the actor was removed after the chain started", tc.part, got)
		}
	}
}

// TestChainDeletedActorHaltKeepsOtherFailures pins that a start failure that
// is not a deleted actor keeps the input halt text unchanged.
func TestChainDeletedActorHaltKeepsOtherFailures(t *testing.T) {
	t.Parallel()

	inner := errors.New("runner refused the spawn")
	err := fmt.Errorf("binding %q builder: %w", "shop-rev", inner)
	fallback := "builder spawn failed: " + err.Error()
	if got := chainDeletedActorHalt("reviewer", "shop", "reviewer", err, fallback); got != fallback {
		t.Errorf("non-unknown-actor halt = %q, want the input %q unchanged", got, fallback)
	}
}

// TestChainDeletedActorHaltRefusesAWrongFlag pins that a part with no resume
// flag of its own never produces a command naming one: a custom workflow
// part reads as its param, and a member with no part keeps the input text.
func TestChainDeletedActorHaltRefusesAWrongFlag(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("binding %q builder: %w", "shop-scout", resolve.ErrUnknownRole)
	fallback := "builder spawn failed: " + err.Error()
	got := chainDeletedActorHalt("scout", "shop", "scout", err, fallback)
	if !strings.Contains(got, "relevo chain --resume --name shop --param scout=<other>") {
		t.Errorf("custom part halt = %q, want the --param spelling", got)
	}
	if strings.Contains(got, "--scout-actor") {
		t.Errorf("custom part halt = %q, want no --scout-actor flag", got)
	}
	for _, tc := range [][3]string{{"", "shop", "scout"}, {"reviewer", "", "reviewer"}, {"reviewer", "shop", ""}} {
		if got := chainDeletedActorHalt(tc[0], tc[1], tc[2], err, fallback); got != fallback {
			t.Errorf("halt(%q, %q, %q) = %q, want the input unchanged", tc[0], tc[1], tc[2], got)
		}
	}
}

// TestChainSendNamesResumeWhenMemberActorIsGone drives the chain send path
// with the reviewer's actor deleted after the start: the member goes NEEDS
// YOU with the resume command, not the bare spawn failure. The reviewer runs
// the custom assistant actor, which a file registry can actually lose: the
// built-in reviewer would survive the row's deletion.
func TestChainSendNamesResumeWhenMemberActorIsGone(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{ReviewerActor: "assistant"})

	rows := chainRows()
	delete(rows, "assistant")
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		m, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		_, err = sendChainRound(context.Background(), rt, tx, m, "review the round")
		return err
	})
	if err == nil {
		t.Fatal("sendChainRound with a deleted member actor succeeded, want the start failure")
	}
	if !errors.Is(err, resolve.ErrUnknownRole) {
		t.Fatalf("sendChainRound err = %v, want the deleted actor", err)
	}

	b := chainBinding(t, rt, "shop-rev")
	if b.State != store.StateNeedsYou {
		t.Errorf("state = %q, want NEEDS YOU", b.State)
	}
	want := "relevo chain --resume --name shop --reviewer-actor <other>"
	if !strings.Contains(b.Halt, want) {
		t.Errorf("halt = %q, want it to name %q", b.Halt, want)
	}
	if !strings.Contains(b.Halt, `actor "assistant" was removed after chain "shop" started`) {
		t.Errorf("halt = %q, want it to say the actor was removed after the chain started", b.Halt)
	}
}

// TestChainDeletedActorPartKeepsFallbackForNonMember pins that a name in no
// chain reads as no part, so its halt keeps the bare spawn failure instead
// of naming a resume for a part it cannot prove.
func TestChainDeletedActorPartKeepsFallbackForNonMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	var part, chainName string
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		part, chainName = chainDeletedActorPart(tx, "no-such-member")
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if part != "" || chainName != "" {
		t.Fatalf("chainDeletedActorPart(non-member) = (%q, %q), want (%q, %q)", part, chainName, "", "")
	}

	err := fmt.Errorf("binding %q builder: %w", "no-such-member", resolve.ErrUnknownRole)
	fallback := "builder spawn failed: " + err.Error()
	if got := chainDeletedActorHalt(part, chainName, "ghost", err, fallback); got != fallback {
		t.Errorf("non-member halt = %q, want the input %q unchanged", got, fallback)
	}
}

// TestChainHaltNamesResumeWhenReviewerActorIsGone drives the workflow engine
// to the review with the reviewer's actor deleted after the start: the
// chain's own halt reason, the one wait and status print, names the resume
// that swaps in another actor.
func TestChainHaltNamesResumeWhenReviewerActorIsGone(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{ReviewerActor: "assistant"})

	rows := chainRows()
	delete(rows, "assistant")
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q (%q), want halted on the reviewer's start", row.Status, row.Reason)
	}
	want := "relevo chain --resume --name shop --reviewer-actor <other>"
	if !strings.Contains(row.Reason, want) {
		t.Errorf("halt reason = %q, want it to name %q", row.Reason, want)
	}
	if !strings.Contains(row.Reason, "member shop-rev could not start") {
		t.Errorf("halt reason = %q, want it to keep the member that could not start", row.Reason)
	}
}
