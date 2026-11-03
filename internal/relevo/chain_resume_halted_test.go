package relevo

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// haltedChain halts a chain through its builder: the builder's open round
// closes with a halted report, which is the halt a builder's own failure
// leaves. The member round is closed; the chain is halted awaiting the
// builder's round 1.
func haltedChain(t *testing.T, rt Runtime) {
	t.Helper()
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
}

// TestChainResumeReviewsAManualRoundOnAHaltedChain pins the halted variant of
// the manual-round rule: a builder round sent while the chain is halted and
// closed done is reviewed rather than re-run, so the resume steps to
// reviewing with the reviewer seeded and the builder un-resent.
func TestChainResumeReviewsAManualRoundOnAHaltedChain(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltedChain(t, rt)

	// A manual round: a send to the member is allowed once the chain halted,
	// and its close is not a chain transition.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the halt: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want it still halted after a manual round", row.Status)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepReviewing) {
		t.Errorf("step = %q, want reviewing: the newer manual round is reviewed", row.Step)
	}
	rev := chainBinding(t, rt, "shop-rev")
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != rev.Round {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer, rev.Round)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), rev.Round) {
		t.Error("the reviewer must be seeded for the manual round")
	}
	// The builder is not re-sent: its newest round was already reviewed.
	if builder := chainBinding(t, rt, "shop"); HasPromptEntry(chainLog(t, rt, "shop"), builder.Round) {
		t.Errorf("the builder's round %d must not be open: the resume reviews, it does not re-send", builder.Round)
	}
}

// haltedServedChain starts a served chain and halts it through its builder:
// plan 1 is admitted so the round really runs, then its halted close leaves
// the chain halted with the builder's round 1 closed, the shape a served halt
// leaves behind.
func haltedServedChain(t *testing.T, rt Runtime) {
	t.Helper()
	servedChainStart(t, rt, servedChainRequest("shop"))
	if err := Admit(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("Admit plan 1: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
}

// TestChainResumeReviewsAManualRoundOnAServedHaltedChain runs the local script
// against a served chain: the manual send is the deferred stage the server's
// round start performs, and the resume must review the newer closed round
// rather than re-run the build.
func TestChainResumeReviewsAManualRoundOnAServedHaltedChain(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	haltedServedChain(t, rt)

	// A manual round: the deferred send the server's round start performs is
	// accepted on the halted chain, and the admit starts it.
	res, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{Defer: true})
	if err != nil {
		t.Fatalf("Send after the halt: %v", err)
	}
	if res.Round != 2 {
		t.Fatalf("send round = %d, want the manual round 2", res.Round)
	}
	if builder := chainBinding(t, rt, "shop"); builder.QueuedAt.IsZero() {
		t.Fatal("the manual round is not queued: QueuedAt is zero")
	}
	if err := Admit(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("Admit the manual round: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want it still halted after a manual round", row.Status)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepReviewing) {
		t.Errorf("step = %q, want reviewing: the newer manual round is reviewed", row.Step)
	}
	rev := chainBinding(t, rt, "shop-rev")
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != rev.Round {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer, rev.Round)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), rev.Round) {
		t.Error("the reviewer must be seeded for the manual round")
	}
	// The builder is not re-sent: its newest round was already reviewed.
	if builder := chainBinding(t, rt, "shop"); HasPromptEntry(chainLog(t, rt, "shop"), builder.Round) {
		t.Errorf("the builder's round %d must not be open: the resume reviews, it does not re-send", builder.Round)
	}
}

// TestChainResumeReviewsAManualRoundOnAHaltedMirror runs the script from the
// client's side of a served chain: the halted view settles the mirror, the
// manual send to the halted member ships instead of refusing on the mirror
// row, and the resume posts and pulls the server's review of the manual
// round. The server's own transitions stay the fake's answers; the
// server-runtime test above pins them for real.
func TestChainResumeReviewsAManualRoundOnAHaltedMirror(t *testing.T) {
	t.Parallel()

	// The server halted with the builder's round 1 closed.
	fr := chainPullFake(chainPullView("shop", string(chain.StatusHalted), 1, 0, 0))
	fr.startRoundResp = remote.BindingView{RoundState: remote.RoundRunning}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	// A manual send ships through the server: the fake stands in for the
	// wire, so the send files nothing local and only proves the preflight
	// let it through and the round shipped.
	rt.RemoteSend = RemoteSendDeps{
		Send: func(ctx context.Context, rt Runtime, b store.Binding, planBody []byte, tier, builder string, force bool) (SendResult, error) {
			if _, err := rt.Remote.StartRound(ctx, b.Builder.Server, b.Name, b.Round, planBody, nil, tier, builder, force, nil, false, nil); err != nil {
				return SendResult{}, err
			}
			return SendResult{Round: b.Round}, nil
		},
	}

	pullRounds(t, rt, "shop")
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("mirror status = %q, want the pulled halt", row.Status)
	}

	res, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{})
	if err != nil {
		t.Fatalf("Send to the halted member = %v, want it accepted", err)
	}
	if res.Round != 2 {
		t.Fatalf("send round = %d, want the manual round 2", res.Round)
	}
	if !slices.Contains(fr.calls, "StartRound:zen:shop:2") {
		t.Errorf("ship calls = %v, want the manual round shipped to zen", fr.calls)
	}

	// The server closes the manual round done and stays halted.
	fr.getChainResp = chainPullView("shop", string(chain.StatusHalted), 2, 0, 0)
	pullRounds(t, rt, "shop")
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("mirror status = %q, want it still halted after the manual round", row.Status)
	}

	// The server resumes and reviews the manual round.
	review := chainPullView("shop", string(chain.StatusRunning), 2, 0, 0)
	review.Step = string(chain.StepReviewing)
	review.AwaitingMember = chain.MemberReviewer
	review.AwaitingRound = 1
	fr.getChainResp = review

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if n := countCalls(fr, "ChainResume:zen:shop"); n != 1 {
		t.Errorf("ChainResume calls = %d, want 1 posted to zen", n)
	}
	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.Step != string(chain.StepReviewing) {
		t.Errorf("mirror = status %q step %q, want running/reviewing: the resume reviews the manual round",
			row.Status, row.Step)
	}
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != 1 {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (reviewer, 1)",
			row.AwaitingMember, row.AwaitingRound)
	}
}

// TestChainResumeOnAHaltedChainWithAnOpenMemberRound pins the halt that left
// its member round open: a halt planted while the builder's round 1 is still
// open stands for the halts that fire beside a live round. The manual send is
// refused for the live process the halt left behind, and the resume is refused
// for the open round itself, so ending that round first is the contract.
func TestChainResumeOnAHaltedChainWithAnOpenMemberRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	haltFlowAt(t, rt, "build", "check red", workflow.Awaiting{Step: "build", Member: "builder", Round: 1})
	before := chainBinding(t, rt, "shop")
	if entries, err := rt.Store.ReadLog("shop"); err != nil || !roundOpenIn(entries, before.Round) {
		t.Fatalf("test premise: the builder's round %d must be open: %v %+v", before.Round, before, entries)
	}

	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); !errors.Is(err, ErrBuilderBusy) {
		t.Errorf("Send onto the halted chain's open round = %v, want the busy refusal for the live round", err)
	}
	if after := chainBinding(t, rt, "shop"); after.Round != before.Round {
		t.Errorf("builder round = %d, want the open round %d kept by the refusal", after.Round, before.Round)
	}

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	var open *RoundOpenError
	if !errors.As(err, &open) {
		t.Fatalf("ChainResume = %v, want a *RoundOpenError for the open round", err)
	}
	if open.Member != "shop" || open.Round != before.Round {
		t.Errorf("refusal = (%s, %d), want (shop, %d)", open.Member, open.Round, before.Round)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Errorf("chain status = %q, want it still halted after the refusal", row.Status)
	}
}
