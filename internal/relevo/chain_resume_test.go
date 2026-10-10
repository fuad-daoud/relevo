package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// stoppedChain starts a chain and stops it through the member: the active
// builder's open round is ended the way `relevo stop` ends one, which raises the
// stopped event and leaves the chain stopped, step building, awaiting the
// builder. That is the state a resume exists for.
func stoppedChain(t *testing.T, rt Runtime, opts ChainOptions) {
	t.Helper()
	startedChain(t, rt, opts)
	if _, err := Stop(context.Background(), rt, "shop", StopOptions{}); err != nil {
		t.Fatalf("Stop shop: %v", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusStopped) {
		t.Fatalf("chain status = %q, want stopped", row.Status)
	}
}

// TestChainResumeReSendsTheHaltedStep pins the plain resume: a chain stopped
// while building re-runs the building step -- the plan is handed to the builder
// again -- and the chain is running with the new round as its awaiting one.
func TestChainResumeReSendsTheHaltedStep(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})

	res, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.Step != string(chain.StepBuilding) {
		t.Errorf("chain = status %q step %q, want running/building", row.Status, row.Step)
	}
	builder := chainBinding(t, rt, "shop")
	if row.AwaitingMember != chain.MemberBuilder || row.AwaitingRound != builder.Round {
		t.Errorf("awaiting = (%s, %d), want the builder's new round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberBuilder, builder.Round)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop"), builder.Round) {
		t.Errorf("the builder's round %d must be open after the resume", builder.Round)
	}
	if res.Chain.Status != string(chain.StatusRunning) || res.Chain.AwaitingRound != builder.Round {
		t.Errorf("result chain = %+v, want the resumed row", res.Chain)
	}
	if len(res.Members) != 3 {
		t.Errorf("result members = %d, want the chain's three", len(res.Members))
	}
}

// TestChainResumeReviewsANewerManualRound pins the manual-round rule: a builder
// round that closed after the chain stopped -- a send a human made while the
// chain was down -- is reviewed rather than re-run, so the chain steps to
// reviewing and the reviewer is seeded for that round.
func TestChainResumeReviewsANewerManualRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})

	// A manual round: a send to the member is allowed once the chain is
	// stopped, and its close is not a chain transition.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the stop: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusStopped) {
		t.Fatalf("chain status = %q, want it still stopped after a manual round", row.Status)
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

// TestChainResumeResetsCorrections pins the human's reset: a chain that halted
// with correction rounds spent comes back with the count at 0, because a human
// has looked at the plan.
func TestChainResumeResetsCorrections(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	// One correction round, then a builder round that halts: the chain holds
	// a spent correction when the resume arrives.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())
	if row := chainStoredRow(t, rt, "shop"); row.Corrections != 1 {
		t.Fatalf("corrections = %d, want the one spent correction", row.Corrections)
	}
	chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
	halted := chainStoredRow(t, rt, "shop")
	if halted.Status != string(chain.StatusHalted) || halted.Corrections != 1 {
		t.Fatalf("chain = status %q corrections %d, want halted with 1", halted.Status, halted.Corrections)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Corrections != 0 {
		t.Errorf("corrections = %d, want 0: a resume resets the count", row.Corrections)
	}
	if row.Status != string(chain.StatusRunning) || row.Plan != 1 {
		t.Errorf("chain = status %q plan %d, want running on plan 1", row.Status, row.Plan)
	}
}

// TestChainResumeRefusesRunningOrDone pins the two refusals and the missing
// name: `--resume` continues a chain a human has looked at, so a running chain
// and a finished chain are both refused, and a name the store does not hold is
// the ordinary not-found.
func TestChainResumeRefusesRunningOrDone(t *testing.T) {
	t.Parallel()

	t.Run("running", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err == nil {
			t.Fatal("ChainResume on a running chain = nil, want a refusal")
		} else if !strings.Contains(err.Error(), "chain shop is running") {
			t.Errorf("err = %v, want it to say the chain is running", err)
		} else if !errors.Is(err, ErrChainRunning) {
			t.Errorf("err = %v, want errors.Is(err, ErrChainRunning): the refusal is a conflict, not internal", err)
		}
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
			t.Errorf("chain status = %q, want it untouched", row.Status)
		}
	})

	t.Run("done", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
			t.Fatalf("chain status = %q, want done", row.Status)
		}

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err == nil {
			t.Fatal("ChainResume on a done chain = nil, want a refusal")
		} else if !strings.Contains(err.Error(), "chain shop is done") {
			t.Errorf("err = %v, want it to say the chain is done", err)
		} else if !errors.Is(err, ErrChainDone) {
			t.Errorf("err = %v, want errors.Is(err, ErrChainDone): the refusal is a conflict, not internal", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "nope"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ChainResume(nope) = %v, want store.ErrNotFound", err)
		}
	})
}

// TestChainResumeRefusesAnOpenMemberRound pins item 1: a resume whose target
// member already has a round open refuses with the send path's RoundOpenError
// before any write -- no junk halt trace row, no staged round, and the chain
// row unchanged. Both the builder and the reviewer are covered; the reviewer
// case
// is the live shape where a newer manual builder round would be reviewed while
// a manual reviewer round is open.
func TestChainResumeRefusesAnOpenMemberRound(t *testing.T) {
	t.Parallel()

	t.Run("builder round open", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		// A manual round on the builder, sent after the stop and left open.
		if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
			t.Fatalf("manual Send to the builder: %v", err)
		}
		assertResumeOpenRoundRefused(t, rt, chain.MemberBuilder)
	})

	t.Run("reviewer round open", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		// A manual builder round that closes, so the resume would review it...
		if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
			t.Fatalf("manual Send to the builder: %v", err)
		}
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		// ...while a manual reviewer round is open: the resume's target is the
		// reviewer, whose own round is in flight.
		if _, err := Send(context.Background(), rt, "shop-rev", writePlan(t, "review this"), SendOptions{}); err != nil {
			t.Fatalf("manual Send to the reviewer: %v", err)
		}
		assertResumeOpenRoundRefused(t, rt, chain.MemberReviewer)
	})
}

// assertResumeOpenRoundRefused resumes a chain whose target member has an open
// round and pins the refusal: the RoundOpenError names the member and its open
// round, no trace row was added, the chain row is unchanged and the member's
// round is the one that was open.
func assertResumeOpenRoundRefused(t *testing.T, rt Runtime, part string) {
	t.Helper()

	memberName := "shop"
	if part == chain.MemberReviewer {
		memberName = "shop-rev"
	}
	want := chainBinding(t, rt, memberName)
	before := chainStoredRow(t, rt, "shop")
	traceBefore := len(chainTrace(t, rt, "shop"))

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	var open *RoundOpenError
	if !errors.As(err, &open) {
		t.Fatalf("ChainResume = %v, want a *RoundOpenError", err)
	}
	if open.Member != memberName || open.Round != want.Round {
		t.Errorf("refusal = (%s, %d), want (%s, %d)", open.Member, open.Round, memberName, want.Round)
	}

	if after := chainStoredRow(t, rt, "shop"); !reflect.DeepEqual(before, after) {
		t.Errorf("chain row changed by the refusal:\nbefore %+v\nafter  %+v", before, after)
	}
	if got := len(chainTrace(t, rt, "shop")); got != traceBefore {
		t.Errorf("trace rows = %d, want the %d before the refusal: no junk halt row", got, traceBefore)
	}
	if b := chainBinding(t, rt, memberName); b.Round != want.Round {
		t.Errorf("%s round = %d, want %d untouched", memberName, b.Round, want.Round)
	}
}

// TestChainResumeAppliesOnlyGivenFlags pins the flag rule on the stored
// settings: the flag that was given replaces its setting, and every setting
// whose flag was left alone keeps the value the chain was started with.
func TestChainResumeAppliesOnlyGivenFlags(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{MaxCorrections: ptr(3), Security: ptr(false)})
	before := storedSettings(t, chainStoredRow(t, rt, "shop"))

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name: "shop", MaxCorrections: ptr(1),
	}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	after := storedSettings(t, chainStoredRow(t, rt, "shop"))
	if after.MaxCorrections != 1 {
		t.Errorf("MaxCorrections = %d, want the flag's 1", after.MaxCorrections)
	}
	if after.ReviewerActor != before.ReviewerActor || after.PlannerActor != before.PlannerActor ||
		after.SecurityActor != before.SecurityActor || after.Security != before.Security {
		t.Errorf("settings = %+v, want every unflagged value kept from %+v", after, before)
	}
}

// TestChainResumeGateFlagsUpdateTheBuilder pins the gate flags' half on a
// workflow chain: an explicit --gate/--regate writes the stored settings and the
// workflow's params, a --no-gate clears both, and a resume that names none keeps
// the stored check.
func TestChainResumeGateFlagsUpdateTheBuilder(t *testing.T) {
	t.Parallel()

	t.Run("gate and regate", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{
			Name: "shop", Gate: "go test ./...", Regate: ptr(1),
		}); err != nil {
			t.Fatalf("ChainResume: %v", err)
		}

		row := chainStoredRow(t, rt, "shop")
		set := storedSettings(t, row)
		if set.Gate != "go test ./..." || set.Regate != 1 {
			t.Errorf("settings gate/regate = %q/%d, want go test ./.../1", set.Gate, set.Regate)
		}
		if got := storedFlowParam(t, row, "gate"); got != "go test ./..." {
			t.Errorf("workflow gate param = %q, want go test ./...", got)
		}
	})

	t.Run("no-gate clears both", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{Gate: "make check", Regate: ptr(2)})

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", NoGate: true}); err != nil {
			t.Fatalf("ChainResume: %v", err)
		}

		row := chainStoredRow(t, rt, "shop")
		if set := storedSettings(t, row); set.Gate != "" {
			t.Errorf("settings gate = %q, want none", set.Gate)
		}
		if got := storedFlowParam(t, row, "gate"); got != "" {
			t.Errorf("workflow gate param = %q, want none", got)
		}
	})

	t.Run("none keeps the stored check", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{Gate: "make check", Regate: ptr(2)})

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
			t.Fatalf("ChainResume: %v", err)
		}

		row := chainStoredRow(t, rt, "shop")
		if set := storedSettings(t, row); set.Gate != "make check" || set.Regate != 2 {
			t.Errorf("settings gate/regate = %q/%d, want the stored make check/2", set.Gate, set.Regate)
		}
		if got := storedFlowParam(t, row, "gate"); got != "make check" {
			t.Errorf("workflow gate param = %q, want the stored make check", got)
		}
	})
}

// TestChainResumeCreatesTheSecurityMemberWhenTurnedOn pins the late member:
// turning the security phase on for a chain started without it creates the
// reader member now, beside the builder, and names it on the chain row.
func TestChainResumeCreatesTheSecurityMemberWhenTurnedOn(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{Security: ptr(false)})
	if row := chainStoredRow(t, rt, "shop"); row.Security != "" {
		t.Fatalf("Security = %q, want none before the resume", row.Security)
	}

	res, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Security: ptr(true)})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Security != "shop-sec" {
		t.Fatalf("Security = %q, want shop-sec", row.Security)
	}
	sec := chainBinding(t, rt, "shop-sec")
	if sec.Shape != store.ShapeReader || sec.Role != "security" {
		t.Errorf("security member = %q/%q, want a reader running security", sec.Role, sec.Shape)
	}
	if sec.Worktree != "" {
		t.Errorf("security member worktree = %q, want none: a reader shares the builder's tree", sec.Worktree)
	}
	if builder := chainBinding(t, rt, "shop"); sec.CWD != builder.CWD {
		t.Errorf("security member CWD = %q, want the builder's tree %q", sec.CWD, builder.CWD)
	}
	if !storedSettings(t, row).Security {
		t.Error("stored settings = security off, want the flag's on")
	}
	found := false
	for _, m := range res.Members {
		if m.Name == "shop-sec" {
			found = true
		}
	}
	if !found {
		t.Errorf("result members = %+v, want the late security member", res.Members)
	}
}

// TestChainResumeWritesOneTraceRow pins the trace: a resume writes exactly one
// row, at the next seq, carrying the resumed event and the send it performed.
func TestChainResumeWritesOneTraceRow(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})
	before := chainTrace(t, rt, "shop")
	if len(before) != 1 {
		t.Fatalf("trace before the resume = %+v, want the stop's one row", before)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	after := chainTrace(t, rt, "shop")
	if len(after) != len(before)+1 {
		t.Fatalf("trace = %+v, want exactly one row more than %d", after, len(before))
	}
	row := after[len(after)-1]
	if row.Seq != len(after) {
		t.Errorf("resume row seq = %d, want %d", row.Seq, len(after))
	}
	if row.Phase != "" || row.Step != "build" {
		t.Errorf("resume row = phase %q step %q, want the build step with no legacy phase", row.Phase, row.Step)
	}
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != workflow.EventNeedsYou || ev.Step != "build" {
		t.Errorf("event = %+v, want a needs_you on build", ev)
	}
	act, err := workflow.DecodeAction(row.Action)
	if err != nil {
		t.Fatalf("DecodeAction: %v", err)
	}
	if act.Kind != workflow.ActionSend || act.Actor != "builder" {
		t.Errorf("action = %+v, want the send to the builder", act)
	}
	if builder := chainBinding(t, rt, "shop"); row.Round != builder.Round {
		t.Errorf("resume row round = %d, want the round it sent %d", row.Round, builder.Round)
	}
}

// TestChainResumeTraceRowNamesTheStepItMovedTo pins the resume's trace wording:
// the row's reason says where the resume moved the chain, in the trace's own
// step words -- a re-sent build round reads `resumed -> build`, a reviewed
// manual round `resumed -> review` -- in the stored event and the rendered line.
func TestChainResumeTraceRowNamesTheStepItMovedTo(t *testing.T) {
	t.Parallel()

	t.Run("a re-sent build round", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
			t.Fatalf("ChainResume: %v", err)
		}
		assertResumeRowStep(t, rt, "build")
	})

	t.Run("a reviewed manual round", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
			t.Fatalf("Send after the stop: %v", err)
		}
		chainBuilderClose(t, rt, "shop", chainDoneBody())

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
			t.Fatalf("ChainResume: %v", err)
		}
		assertResumeRowStep(t, rt, "review")
	})
}

// assertResumeRowStep pins that the chain's last trace row carries a needs_you
// event naming want, the step the resume moved to, in the stored event and the
// rendered line.
func assertResumeRowStep(t *testing.T, rt Runtime, want string) {
	t.Helper()

	events := chainTrace(t, rt, "shop")
	last := events[len(events)-1]
	ev, err := workflow.DecodeEvent(last.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != workflow.EventNeedsYou || ev.Step != want {
		t.Errorf("resume event = %+v, want a needs_you on %q", ev, want)
	}
	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if out := RenderTrace(doc); !strings.Contains(out, "needs you") {
		t.Errorf("RenderTrace = %q, want it to carry the resume's needs_you row", out)
	}
}

// TestChainResumeHaltsWhenTheSendFails pins the failed re-run: a member that
// cannot start leaves the chain halted with the member's own reason, rather
// than running and awaiting a round that never opened.
func TestChainResumeHaltsWhenTheSendFails(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})

	// A symlinked plan path: the round cannot be staged, so the send fails
	// before the member is touched.
	builder := chainBinding(t, rt, "shop")
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Symlink(sentinel, rt.Store.PromptPath("shop", builder.Round)); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err == nil {
		t.Fatal("ChainResume = nil, want the send failure")
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Errorf("chain status = %q, want halted after a failed send", row.Status)
	}
	if !strings.Contains(row.Reason, "could not start") {
		t.Errorf("halt reason = %q, want it to name the member that could not start", row.Reason)
	}
}

// TestChainResumeRemoteBuilderShipsThroughThePendingStep pins the remote
// resume: the resumed round is staged like any send, and the unlocked step
// ships it -- one StartRound with verify off, the prompt entry with the chain
// step note, and the chain running on that round.
func TestChainResumeRemoteBuilderShipsThroughThePendingStep(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	seedRemoteChain(t, rt, "shop", remoteChainOpts{})
	advanceRemoteChain(t, rt, 2)
	haltRemoteChain(t, rt)

	fr.calls = nil
	res, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	if !slices.Contains(fr.calls, "StartRound:zen:shop:2") {
		t.Errorf("calls = %v, want the resumed round shipped to zen", fr.calls)
	}
	if fr.startRoundVerify == nil || *fr.startRoundVerify {
		t.Errorf("verify = %v, want an explicit false", fr.startRoundVerify)
	}
	if string(fr.startRoundPlan) != "plan 1\n" {
		t.Errorf("shipped plan = %q, want the plan copy", fr.startRoundPlan)
	}
	if !promptNoteFor(chainLog(t, rt, "shop"), 2, "chain builder") {
		t.Errorf("log = %+v, want the round-2 prompt entry with the chain step note", chainLog(t, rt, "shop"))
	}
	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.AwaitingMember != chain.MemberBuilder || row.AwaitingRound != 2 {
		t.Errorf("chain = status %q awaiting (%s, %d), want running on the builder's round 2",
			row.Status, row.AwaitingMember, row.AwaitingRound)
	}
	if res.Chain.Status != string(chain.StatusRunning) {
		t.Errorf("result chain = %+v, want the resumed row", res.Chain)
	}
}

// TestChainCorrectionSeedNamesTheJudgedBuilderRound pins the manual-round
// resume: after a resume reviews a newer manual builder round and the reviewer
// asks for changes, the correction seed names that builder round -- its report,
// the copies of its diff and cumulative diff, and its prompt -- not the
// reviewer's own round.
func TestChainCorrectionSeedNamesTheJudgedBuilderRound(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	stoppedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	// A manual round while the chain is stopped: it closes without a chain
	// transition, so its record is the one a later resume reviews.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the stop: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	builderRound := chainBinding(t, rt, "shop").Round - 1
	if builderRound != 2 {
		t.Fatalf("manual builder round = %d, want 2", builderRound)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	planner := chainBinding(t, rt, "shop-plan")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-plan", planner.Round))
	if err != nil {
		t.Fatalf("read the correction prompt: %v", err)
	}
	got := string(text)
	diffCopy, ok := rt.Store.ChainInputPath("shop", rt.Store.DiffPath("shop", builderRound))
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", rt.Store.DiffPath("shop", builderRound))
	}
	planDiffCopy, ok := rt.Store.ChainInputPath("shop", rt.Store.PlanDiffPath("shop", builderRound))
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", rt.Store.PlanDiffPath("shop", builderRound))
	}
	for _, want := range []string{
		seedCopy(t, rt, rt.Store.ReportPath("shop", builderRound)),
		"This round's diff: " + diffCopy,
		"This round's prompt: " + seedCopy(t, rt, rt.Store.PromptPath("shop", builderRound)),
		"Plan diff, every round of this plan so far: " + planDiffCopy,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("correction seed does not name %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, rt.Store.DiffPath("shop", builderRound)) || strings.Contains(got, rt.Store.PlanDiffPath("shop", builderRound)) {
		t.Errorf("correction seed names a round_file key:\n%s", got)
	}
}

// TestChainResumeReviewKeepsThePlanStartCommit pins the resume's start: a
// manual round's review diffs from the commit the chain stored, to the manual
// round's own closed tree, even though the head moved before the resume.
func TestChainResumeReviewKeepsThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	stoppedChain(t, rt, ChainOptions{})
	stored := chainStoredRow(t, rt, "shop").PlanStartCommit
	if stored == "" {
		t.Fatal("test premise: the chain must record a plan-start commit")
	}

	// The manual round is sent at a different head from the stored plan start,
	// so a commit recomputed at the resume would be visible.
	fg.headCommitID = "head-manual"
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the stop: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	// A moved head must not be picked up: the stored commit is the plan start.
	fg.headCommitID = "head-moved"
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	if fg.lastDiffFrom != stored {
		t.Errorf("the cumulative diff ran from %q, want the stored plan start %q", fg.lastDiffFrom, stored)
	}
	if fg.lastDiffTo != "tree-end" {
		t.Errorf("the cumulative diff ran to %q, want the manual round's closed tree %q", fg.lastDiffTo, "tree-end")
	}
}

// TestChainResumeReSendKeepsThePlanStartCommit pins the re-send half: resuming
// a stopped build round hands the plan to the builder again and leaves the
// recorded start untouched.
func TestChainResumeReSendKeepsThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})
	before := chainStoredRow(t, rt, "shop").PlanStartCommit
	if before == "" {
		t.Fatal("test premise: the chain must record a plan-start commit")
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepBuilding) {
		t.Fatalf("step = %q, want the building step re-sent", row.Step)
	}
	if row.PlanStartCommit != before {
		t.Errorf("plan start after a re-send = %q, want it held at %q", row.PlanStartCommit, before)
	}
}

// TestChainResumeReSendsTheStoppedRoundsOwnPrompt pins item 6: a resume whose
// action is a builder send re-sends the awaited round's own staged prompt --
// the exact bytes the stopped round was handed -- so a correction or repair
// round comes back, not the chain's plan copy.
func TestChainResumeReSendsTheStoppedRoundsOwnPrompt(t *testing.T) {
	t.Parallel()

	t.Run("a stopped correction round", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
		chainReaderClose(t, rt, "shop-plan", "# Correction plan\n\nDo it.\n")

		// The builder's correction round is open; stop the chain there.
		if _, err := Stop(context.Background(), rt, "shop", StopOptions{}); err != nil {
			t.Fatalf("Stop the correction round: %v", err)
		}
		assertResumeReSendsTheStagedPrompt(t, rt, "# Correction plan")
	})

	t.Run("a stopped repair round", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{Gate: "make check", Regate: ptr(2)})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainRedCheck(t, rt, "shop", "FAIL the same thing\n")

		// The repair round is open; stop the chain there.
		if _, err := Stop(context.Background(), rt, "shop", StopOptions{}); err != nil {
			t.Fatalf("Stop the repair round: %v", err)
		}
		assertResumeReSendsTheStagedPrompt(t, rt, "# Repair round")
	})

	t.Run("a stopped repair round with a replaced gate", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{Gate: "make check", Regate: ptr(2)})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainRedCheck(t, rt, "shop", "FAIL the same thing\n")

		// The repair round is open; stop the chain there, then resume with a
		// replaced check.
		if _, err := Stop(context.Background(), rt, "shop", StopOptions{}); err != nil {
			t.Fatalf("Stop the repair round: %v", err)
		}
		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "make check"}); err != nil {
			t.Fatalf("ChainResume with a replaced gate: %v", err)
		}

		resent := chainBinding(t, rt, "shop")
		got, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", resent.Round))
		if err != nil {
			t.Fatalf("read the re-sent round's prompt: %v", err)
		}
		if strings.Contains(string(got), repairPlanPrefix) {
			t.Errorf("the re-sent round still carries the stale repair prompt:\n%s", got)
		}
		planCopy, err := rt.Store.ReadFile(rt.Store.ChainPlanPath("shop", 1))
		if err != nil {
			t.Fatalf("read the plan copy: %v", err)
		}
		if string(got) != string(planCopy) {
			t.Errorf("the re-sent round is not the step's seed under the new gate:\ngot:\n%s\nwant:\n%s", got, planCopy)
		}
	})
}

// TestChainResumeSupersedesTheStaleEndDelivery pins the delivery bookkeeping: a
// halt queues the chain's end payload, and a resume must confirm it so a
// resolved chain cannot push a stale NEEDS YOU later.
func TestChainResumeSupersedesTheStaleEndDelivery(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainHaltedBody("the step failed"))

	pendingChain := func() int {
		t.Helper()
		n := 0
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			entries, err := tx.PendingForMasterMindThrough("shop", 0)
			if err != nil {
				return err
			}
			for _, p := range entries {
				if p.Entry.Kind == store.KindChain {
					n++
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("read pending entries: %v", err)
		}
		return n
	}
	if pendingChain() == 0 {
		t.Fatal("test premise: the halt must queue the chain's end delivery")
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if n := pendingChain(); n != 0 {
		t.Errorf("pending chain deliveries after a resume = %d, want 0", n)
	}
}

// TestChainResumeClosesADeadMemberRound pins the wedge fix: a member round
// that died without a close leaves a halted chain with no working command --
// the resume refuses an open round and the stop path refuses a halted one. The
// resume now closes the dead round the way a stop would and re-runs the step.
func TestChainResumeClosesADeadMemberRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// The builder closes green: the reviewer's round opens.
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	before := chainBinding(t, rt, "shop-rev")
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), before.Round) {
		t.Fatal("test premise: the reviewer's round must be open")
	}

	// The reviewer dies without a close: NEEDS YOU and no process, so the next
	// sweep halts the chain on the member -- the state a round that died
	// without a report leaves.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		rev, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		rev, err = haltBinding(context.Background(), rt, tx, rev, rev.Round, "reviewer exited without an output")
		if err != nil {
			return err
		}
		rev.Builder = clearProcess(rev.Builder)
		return tx.Save(rev)
	}); err != nil {
		t.Fatalf("halt the reviewer: %v", err)
	}
	tickChains(context.Background(), rt)
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted on the dead reviewer", row.Status)
	}

	// The resume closes the dead round the way a stop would and re-runs the
	// review.
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	after := chainBinding(t, rt, "shop-rev")
	if after.Round <= before.Round {
		t.Errorf("reviewer round = %d, want a round after %d", after.Round, before.Round)
	}
	if after.State != store.StateActive {
		t.Errorf("reviewer state = %q, want active (a fresh review round)", after.State)
	}
	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.AwaitingMember != chain.MemberReviewer {
		t.Errorf("chain row = %s awaiting %s, want running awaiting reviewer", row.Status, row.AwaitingMember)
	}
}

// TestChainResumeOverAReportlessNewerRoundClosesStoppedAndReRuns pins the
// reportless close: a newer round on the awaited member whose report entry
// exists but whose report file was never written -- a stop close writes exactly
// that -- must not reach the resume as a bare store error. The resume has no
// body to route on, so it closes the round as stopped and re-runs the halted
// step rather than answering internal.
func TestChainResumeOverAReportlessNewerRoundClosesStoppedAndReRuns(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}

	// A manual round sent while the chain was down, then stopped: a stop close
	// records the round as closed without a report, so the round has a report
	// entry and no report file.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("manual Send to the builder: %v", err)
	}
	if _, err := Stop(context.Background(), rt, "shop", StopOptions{}); err != nil {
		t.Fatalf("Stop the manual round: %v", err)
	}
	manual := chainBinding(t, rt, "shop").Round - 1
	assertStopCloseOnRound(t, rt, "shop", manual)

	res, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err != nil {
		t.Fatalf("ChainResume over a reportless round: %v", err)
	}

	// The step is re-run, not reviewed: there was no report to review, so the
	// chain hands the builder a fresh round of its own step.
	row := chainStoredRow(t, rt, "shop")
	builder := chainBinding(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.Step != string(chain.StepBuilding) {
		t.Errorf("chain = status %q step %q, want running/building: a reportless round is re-run, not reviewed", row.Status, row.Step)
	}
	if builder.Round <= manual || row.AwaitingRound != builder.Round {
		t.Errorf("builder round = %d, chain awaits %d, want a fresh round after %d", builder.Round, row.AwaitingRound, manual)
	}
	if !store.RoundOpen(chainLog(t, rt, "shop"), builder.Round) {
		t.Errorf("the builder's round %d must be open: the resume re-runs the step", builder.Round)
	}
	if res.Chain.Status != string(chain.StatusRunning) {
		t.Errorf("result chain = %+v, want the resumed row", res.Chain)
	}
	assertStopCloseOnRound(t, rt, "shop", manual)
}

// TestChainResumeOnAWorkflowlessChainNeverInternal pins the row the engine's
// own conversion cannot leave behind: `chain --resume` read its missing workflow
// or its missing engine state as a plain error, which the CLI reports as
// internal. A row with no workflow is migrated through the same single-row
// conversion the daemon's start-up sweep runs; a row that still carries neither
// a workflow nor a state is refused in the input class, naming its own reason
// and the command that reads it.
func TestChainResumeOnAWorkflowlessChainNeverInternal(t *testing.T) {
	t.Parallel()

	t.Run("a row with no workflow is migrated", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stripChainEngine(t, rt, true)

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
			t.Fatalf("ChainResume over a legacy row: %v", err)
		}

		row := chainStoredRow(t, rt, "shop")
		if row.Status != string(chain.StatusRunning) || len(row.WorkflowJSON) == 0 || len(row.StateJSON) == 0 {
			t.Errorf("chain = status %q with %d workflow bytes and %d state bytes, want it migrated and running",
				row.Status, len(row.WorkflowJSON), len(row.StateJSON))
		}
	})

	t.Run("an unconvertible row is refused, not internal", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stripChainEngine(t, rt, true)
		// Settings the conversion reads and cannot parse: the migration cannot
		// bring this row onto the engine, which is what halts it in the sweep.
		corruptChainSettings(t, rt, "shop")

		_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
		if err == nil {
			t.Fatal("ChainResume over an unconvertible row = nil, want a refusal")
		}
		assertResumeRefusedNotInternal(t, err)
	})

	t.Run("a row with no engine state is refused, not internal", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stripChainEngine(t, rt, false)

		_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
		if err == nil {
			t.Fatal("ChainResume over a state-less row = nil, want a refusal")
		}
		assertResumeRefusedNotInternal(t, err)
	})
}

// stripChainEngine leaves the chain row as one that predates the workflow
// engine: its engine state is gone and, with dropWorkflow, so is its
// definition, leaving the legacy columns and the row's own reason as all it
// holds.
func stripChainEngine(t *testing.T, rt Runtime, dropWorkflow bool) {
	t.Helper()

	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		c.StateJSON = nil
		if dropWorkflow {
			c.WorkflowJSON = nil
			c.Status = string(chain.StatusHalted)
			c.Reason = "could not convert to a workflow: chain shop: unknown legacy step"
		}
		c.UpdatedAt = rt.Now().UTC()
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("strip the engine columns: %v", err)
	}
}

// corruptChainSettings writes settings no chain conversion can read.
func corruptChainSettings(t *testing.T, rt Runtime, name string) {
	t.Helper()

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		c.SettingsJSON = []byte("not json")
		c.UpdatedAt = rt.Now().UTC()
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("corrupt the chain settings: %v", err)
	}
}

// assertResumeRefusedNotInternal pins the class of a resume refusal: the input
// class, so the CLI reports it as refused or conflict and never as an internal
// failure, and it names the command that reads the row.
func assertResumeRefusedNotInternal(t *testing.T, err error) {
	t.Helper()

	if !errors.Is(err, ErrRefused) {
		t.Fatalf("refusal %v is not in the input class: errors.Is(err, ErrRefused) is false, so the CLI reads it as internal", err)
	}
	if !strings.Contains(err.Error(), "relevo status shop") {
		t.Errorf("refusal %q names no working next step", err)
	}
}

// assertStopCloseOnRound pins that the given round was closed the way a stop
// closes one -- a report entry noting no report was written, an unstructured
// outcome and the stop entry beside it -- and returns the report entry's path,
// which is on no disk.
func assertStopCloseOnRound(t *testing.T, rt Runtime, name string, round int) string {
	t.Helper()

	var close store.LogEntry
	stopped := false
	for _, e := range chainLog(t, rt, name) {
		switch {
		case e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round:
			close = e
		case e.Direction == store.DirToMasterMind && e.Kind == store.KindStop && e.Round == round:
			stopped = true
		}
	}
	if close.Path == "" {
		t.Fatalf("%s round %d has no report entry", name, round)
	}
	if !strings.Contains(close.Note, "stopped") || !strings.Contains(close.Note, "noreport") {
		t.Errorf("round %d close note = %q, want the stop close's stopped/noreport", round, close.Note)
	}
	if close.Outcome != reporttail.OutcomeUnstructured {
		t.Errorf("round %d close outcome = %q, want %q: a stop close reports nothing", round, close.Outcome, reporttail.OutcomeUnstructured)
	}
	if !stopped {
		t.Errorf("round %d has no stop entry: its close was not a stop close", round)
	}
	if _, err := os.Stat(close.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the report file %s = %v, want it absent: this round is reportless", close.Path, err)
	}
	return close.Path
}

// assertResumeReSendsTheStagedPrompt stops on a chain awaiting the builder on
// the round whose staged prompt contains marker (the planner's text or the
// repair plan), resumes it, and pins that the re-sent round was handed that
// same text rather than the plan copy.
func assertResumeReSendsTheStagedPrompt(t *testing.T, rt Runtime, marker string) {
	t.Helper()

	awaiting := chainStoredRow(t, rt, "shop").AwaitingRound
	if awaiting == 0 {
		t.Fatal("test premise: the chain must await a nonzero builder round")
	}
	want, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", awaiting))
	if err != nil {
		t.Fatalf("read the stopped round's prompt: %v", err)
	}
	if !strings.Contains(string(want), marker) {
		t.Fatalf("the stopped round's prompt does not carry %q:\n%s", marker, want)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	resent := chainBinding(t, rt, "shop")
	got, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", resent.Round))
	if err != nil {
		t.Fatalf("read the re-sent round's prompt: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("the re-sent round's prompt is not the stopped round's own text:\ngot:\n%s\nwant:\n%s", got, want)
	}
	planCopy, err := rt.Store.ReadFile(rt.Store.ChainPlanPath("shop", 1))
	if err != nil {
		t.Fatalf("read the plan copy: %v", err)
	}
	if string(got) == string(planCopy) {
		t.Errorf("the re-sent round got the plan copy, not the stopped round's own text:\n%s", got)
	}
}

// TestChainResumeConvertsLegacyRow pins the legacy resume path: a halted chain
// row with legacy columns, member bindings, and empty WorkflowJSON/StateJSON
// converts through the legacy conversion and resumes instead of failing with
// chain_engine.go:22 carries no workflow.
func TestChainResumeConvertsLegacyRow(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	plan := writePlan(t, "build something")
	c := legacyChainRow("leg", "halted", "build", "building", "builder", 1, 0)
	c.PlanPathsJSON = []byte(fmt.Sprintf("[%q]", plan))
	members := legacyChainBindings("leg")
	seedLegacyChain(t, rt, c, members)

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "leg"})
	if err != nil {
		t.Fatalf("ChainResume: %v (chain_engine.go:22)", err)
	}

	row := chainStoredRow(t, rt, "leg")
	if len(row.WorkflowJSON) == 0 || len(row.StateJSON) == 0 {
		t.Errorf("row carries workflow %d and state %d bytes, want both", len(row.WorkflowJSON), len(row.StateJSON))
	}
	if row.Status != string(chain.StatusRunning) {
		t.Errorf("chain status = %q, want running", row.Status)
	}
}

// TestChainResumeUnconvertibleLegacyRowStaysHaltedWithReason pins the conversion failure:
// a legacy row that cannot convert keeps today's failure (chain carries no workflow)
// and is halted with the conversion reason in the database.
func TestChainResumeUnconvertibleLegacyRowStaysHaltedWithReason(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	c := legacyChainRow("unconv", "halted", "build", "building", "builder", 1, 0)
	c.SettingsJSON = []byte(`{invalid-json`)
	members := legacyChainBindings("unconv")
	seedLegacyChain(t, rt, c, members)

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "unconv"})
	if err == nil || !strings.Contains(err.Error(), "carries no workflow") {
		t.Fatalf("ChainResume error = %v, want carries no workflow", err)
	}

	row := chainStoredRow(t, rt, "unconv")
	if row.Status != string(chain.StatusHalted) {
		t.Errorf("chain status = %q, want halted", row.Status)
	}
	if !strings.Contains(row.Reason, "could not convert to a workflow") {
		t.Errorf("chain reason = %q, want conversion error reason", row.Reason)
	}
}

// TestChainResumeAlreadyConvertedRowSkipsConversion pins the idempotency: an already-converted
// row retains its exact workflow definition and state bytes.
func TestChainResumeAlreadyConvertedRowSkipsConversion(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Name: "conv"})
	if _, err := Stop(context.Background(), rt, "conv", StopOptions{}); err != nil {
		t.Fatalf("Stop conv: %v", err)
	}
	before := chainStoredRow(t, rt, "conv")

	res, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "conv"})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	_ = res

	after := chainStoredRow(t, rt, "conv")
	if string(after.WorkflowJSON) != string(before.WorkflowJSON) {
		t.Errorf("workflow changed on resume: got %s, want %s", after.WorkflowJSON, before.WorkflowJSON)
	}
}
