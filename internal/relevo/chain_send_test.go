package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestSendChainRoundOpensTheMemberRound pins the helper Send's in-lock core
// became: the member's round is staged, its process started, its prompt entry
// appended with the chain step's note, its baseline recorded, its state active
// and its verify flag cleared.
func TestSendChainRoundOpensTheMemberRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	var sent store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		m, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		sent, err = sendChainRound(context.Background(), rt, tx, m, "review the round")
		return err
	})
	if err != nil {
		t.Fatalf("sendChainRound: %v", err)
	}

	if sent.Round != 1 {
		t.Errorf("round = %d, want 1", sent.Round)
	}
	if sent.State != store.StateActive {
		t.Errorf("state = %q, want active", sent.State)
	}
	if sent.RoundVerify {
		t.Error("a chain member round must never run verify")
	}
	if !sent.FinishPending {
		t.Error("FinishPending must be set, exactly as Send sets it")
	}
	if sent.RoundStartedAt.IsZero() {
		t.Error("RoundStartedAt must be stamped")
	}
	if sent.RoundBaselineHead == "" {
		t.Error("the round's baseline head must be recorded")
	}
	if sent.RoundClosedTree != "" {
		t.Errorf("RoundClosedTree = %q, want empty on a fresh round", sent.RoundClosedTree)
	}

	staged, err := os.ReadFile(rt.Store.PromptPath("shop-rev", 1))
	if err != nil {
		t.Fatalf("read the staged plan: %v", err)
	}
	if string(staged) != "review the round" {
		t.Errorf("staged plan = %q, want the seed text", staged)
	}

	entries := chainLog(t, rt, "shop-rev")
	if !HasPromptEntry(entries, 1) {
		t.Fatalf("reviewer log = %+v, want an open round 1", entries)
	}
	for _, e := range entries {
		if e.Round == 1 && store.IsPromptKind(e.Kind) {
			if !e.Confirmed {
				t.Error("the prompt entry must be confirmed")
			}
			if e.Note != "chain reviewer" {
				t.Errorf("prompt note = %q, want chain reviewer", e.Note)
			}
		}
	}
}

// TestChainMemberRoundsRunWithVerifyOff pins the policy override: with
// policy.verify.default on, a chain-started member round still carries
// RoundVerify == false, because the chain's reviewer replaces the verify
// consult.
func TestChainMemberRoundsRunWithVerifyOff(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rt.Policy = policy.Policy{Verify: &policy.VerifyPolicy{Default: true}}
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	if b.RoundVerify {
		t.Error("a chain member round must run with verify off even when policy.verify.default is on")
	}
	if b.State != store.StateActive {
		t.Errorf("state = %q, want active", b.State)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop"), b.Round) {
		t.Error("plan 1 must be open on the builder")
	}
}

// TestChainStartSendsPlanOneThroughTheChainPath pins that a start's own
// handover goes through the chain sender: the chain row is running and the
// builder's round 1 is open, yet an ordinary manual send to the same member is
// refused. Had the start used the ordinary send, its own refusal would have
// bitten it.
func TestChainStartSendsPlanOneThroughTheChainPath(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("builder = round %d, state %q; want round 1 active", b.Round, b.State)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop"), 1) {
		t.Error("plan 1 must be open on the builder")
	}
	if note := chainLog(t, rt, "shop")[0].Note; !strings.Contains(note, "chain") {
		t.Errorf("builder prompt note = %q, want it to name the chain step", note)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.AwaitingMember != chain.MemberBuilder || row.AwaitingRound != 1 {
		t.Errorf("chain row = %s awaiting (%s, %d); want running awaiting (builder, 1)", row.Status, row.AwaitingMember, row.AwaitingRound)
	}

	// The refusal the start had to avoid exists: a manual send to the same
	// running member is refused.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "x"), SendOptions{}); !errors.Is(err, ErrRunningChainMember) {
		t.Errorf("Send to a running chain member = %v, want ErrRunningChainMember", err)
	}
}

// TestChainReaderRoundsAskForTheChainsOwnBlock pins that a chain reviewer's
// and security member's prompt ends with the chain's own block -- the verdict
// or the finding count -- instead of the generic reader reporttail block, and
// the marker is created before the final message so the round's last text is
// the block itself.
func TestChainReaderRoundsAskForTheChainsOwnBlock(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := rt.Runner.(*fakeRunner)
	startedChain(t, rt, ChainOptions{Security: ptr(true)})

	// Plan 1 closes green: the chain sends the reviewer.
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	reviewerPrompt := ""
	for _, s := range fr.specs {
		j := strings.Join(s.Argv, " ")
		if strings.Contains(j, "verdict: pass") {
			reviewerPrompt = j
		}
	}
	if reviewerPrompt == "" {
		t.Fatalf("no reviewer round carried the verdict block; specs = %d", len(fr.specs))
	}
	for _, want := range []string{"verdict: pass", "create this empty file", "It must end with this block"} {
		if !strings.Contains(reviewerPrompt, want) {
			t.Errorf("reviewer prompt does not carry %q:\n%s", want, reviewerPrompt)
		}
	}
	for _, unwanted := range []string{"status: done", "nothing written after the marker"} {
		if strings.Contains(reviewerPrompt, unwanted) {
			t.Errorf("reviewer prompt still carries %q:\n%s", unwanted, reviewerPrompt)
		}
	}

	// The reviewer passes: the chain sends the security member.
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	securityPrompt := ""
	for _, s := range fr.specs {
		j := strings.Join(s.Argv, " ")
		if strings.Contains(j, "findings: 0") {
			securityPrompt = j
		}
	}
	if securityPrompt == "" {
		t.Fatalf("no security round carried the findings block; specs = %d", len(fr.specs))
	}
	for _, want := range []string{"findings: 0", "create this empty file"} {
		if !strings.Contains(securityPrompt, want) {
			t.Errorf("security prompt does not carry %q:\n%s", want, securityPrompt)
		}
	}
	if strings.Contains(securityPrompt, "status: done") {
		t.Errorf("security prompt still carries the reporttail block:\n%s", securityPrompt)
	}
}

// TestRoundPromptIsChainAwareForEveryStarter pins the one composer every round
// starter uses: a chain member's reader round asks for the chain's own block
// wherever the round starts, while a builder round and a chain planner (whose
// artifact is the plan) keep the ordinary templates.
func TestRoundPromptIsChainAwareForEveryStarter(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Security: ptr(true)})

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		rev, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		if prompt := roundPrompt(rt, tx, rev, "p", "r", "d"); !strings.Contains(prompt, "verdict: pass") || strings.Contains(prompt, "status: done") {
			t.Errorf("chain reviewer prompt is not chain-aware:\n%s", prompt)
		}
		sec, err := tx.Load("shop-sec")
		if err != nil {
			return err
		}
		if prompt := roundPrompt(rt, tx, sec, "p", "r", "d"); !strings.Contains(prompt, "findings: 0") || strings.Contains(prompt, "status: done") {
			t.Errorf("chain security prompt is not chain-aware:\n%s", prompt)
		}
		builder, err := tx.Load("shop")
		if err != nil {
			return err
		}
		if prompt := roundPrompt(rt, tx, builder, "p", "r", "d"); !strings.Contains(prompt, "status: done") {
			t.Errorf("builder prompt lost its reporttail block:\n%s", prompt)
		}
		planner, err := tx.Load("shop-plan")
		if err != nil {
			return err
		}
		if prompt := roundPrompt(rt, tx, planner, "p", "r", "d"); !strings.Contains(prompt, "status: done") {
			t.Errorf("planner prompt must keep the ordinary reader block:\n%s", prompt)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}

// TestSendChainRoundRefusesOpenRound pins that a member round still open is
// refused as a typed RoundOpenError naming the member and the round, so the CLI
// can map it to a conflict with `relevo stop <member>` as the next command.
func TestSendChainRoundRefusesOpenRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// The first send opens shop-rev's round 1.
	var sent store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		m, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		sent, err = sendChainRound(context.Background(), rt, tx, m, "review the round")
		return err
	})
	if err != nil {
		t.Fatalf("first sendChainRound: %v", err)
	}

	// The second send to the same member, its round still open, refuses.
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		m, lerr := tx.Load("shop-rev")
		if lerr != nil {
			return lerr
		}
		_, err := sendChainRound(context.Background(), rt, tx, m, "again")
		return err
	})
	var open *RoundOpenError
	if !errors.As(err, &open) {
		t.Fatalf("second sendChainRound err = %v, want a *RoundOpenError", err)
	}
	if open.Member != "shop-rev" || open.Round != sent.Round {
		t.Errorf("RoundOpenError = %+v, want member shop-rev round %d", open, sent.Round)
	}
	for _, want := range []string{"still open", "relevo stop shop-rev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
		}
	}
}

func TestChainReaderFooterFromDeclaration(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Security: ptr(true)})

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		revBinding, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		revFooter, ok := chainMemberBlock(rt, tx, revBinding)
		if !ok {
			t.Fatal("chainMemberBlock(shop-rev) returned false")
		}
		if want := "verdict: pass   # or: changes"; revFooter != want {
			t.Errorf("reviewer footer = %q, want %q", revFooter, want)
		}

		secBinding, err := tx.Load("shop-sec")
		if err != nil {
			return err
		}
		secFooter, ok := chainMemberBlock(rt, tx, secBinding)
		if !ok {
			t.Fatal("chainMemberBlock(shop-sec) returned false")
		}
		if want := "findings: 0   # a count"; secFooter != want {
			t.Errorf("security footer = %q, want %q", secFooter, want)
		}

		planBinding, err := tx.Load("shop-plan")
		if err != nil {
			return err
		}
		if _, ok := chainMemberBlock(rt, tx, planBinding); ok {
			t.Error("chainMemberBlock(shop-plan) returned true, want false")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}

// TestSendChainRoundRefusedWhenPendingRoundFilePresentWithNoOpenRound pins that
// sendChainRound refuses with ErrReportPending when a member row is at round N,
// no round N is open in the log, but a done marker or report is on disk
// (internal/relevo/chain_send.go:57-59), and that ChainResume halts with that reason.
func TestSendChainRoundRefusedWhenPendingRoundFilePresentWithNoOpenRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// shop-rev has round 1, but its round 1 is not open in the log yet.
	touch(t, rt.Store.DonePath("shop-rev", 1))

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		m, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		_, err = sendChainRound(context.Background(), rt, tx, m, "review the round")
		return err
	})
	if err == nil {
		t.Fatal("sendChainRound must fail when a done marker exists for the round")
	}
	if !errors.Is(err, ErrReportPending) {
		t.Fatalf("err = %v, want it to wrap ErrReportPending", err)
	}
	if !strings.Contains(err.Error(), "001-done exists") {
		t.Errorf("err = %q, want it to name 001-done", err)
	}

	// Close builder round 1 with halt so the chain is halted.
	chainBuilderClose(t, rt, "shop", chainHaltedBody("builder failed"))

	// plant 002-done for the awaited builder so resume's re-send fails
	touch(t, rt.Store.DonePath("shop", 2))

	_, err = ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume must fail when member could not start")
	}
	if !strings.Contains(err.Error(), "002-done exists") || !strings.Contains(err.Error(), ErrReportPending.Error()) {
		t.Fatalf("ChainResume err = %v, want it to contain ErrReportPending and name 002-done exists", err)
	}
	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Errorf("chain row status = %q, want halted", row.Status)
	}
	if !strings.Contains(row.Reason, "002-done exists") || !strings.Contains(row.Reason, ErrReportPending.Error()) {
		t.Errorf("chain row reason = %q, want it to contain 002-done exists and ErrReportPending", row.Reason)
	}
}

// TestSendChainRoundRefusedWhenReportPresentWithNoOpenRound pins that
// sendChainRound also refuses when the report file is present on disk
// without an open round in the log (internal/relevo/chain_send.go:57-59).
func TestSendChainRoundRefusedWhenReportPresentWithNoOpenRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	touch(t, rt.Store.ReportPath("shop-rev", 1))

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		m, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		_, err = sendChainRound(context.Background(), rt, tx, m, "review the round")
		return err
	})
	if err == nil {
		t.Fatal("sendChainRound must fail when a report exists for the round")
	}
	if !errors.Is(err, ErrReportPending) {
		t.Fatalf("err = %v, want it to wrap ErrReportPending", err)
	}
	if !strings.Contains(err.Error(), "001-report.md exists") {
		t.Errorf("err = %q, want it to name 001-report.md", err)
	}
}


