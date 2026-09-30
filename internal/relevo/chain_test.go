package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The report bodies the chain tests write. A builder's body must parse as a
// done round; a reader's body carries the block its close reads.

func chainDoneBody() string {
	return "finished the plan\n\n```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"
}

func chainVerdictBody(verdict string) string {
	return "I reviewed the round.\n\n```relevo\nverdict: " + verdict + "\n```\n"
}

func chainFindingsBody(n int) string {
	return "I scanned the branch.\n\n```relevo\nfindings: " + strconv.Itoa(n) + "\n```\n"
}

func chainNoVerdictBody() string {
	return "I reviewed the round but forgot the block.\n"
}

// chainMember loads one chain member's binding.
func chainBinding(t *testing.T, rt Runtime, name string) store.Binding {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	return b
}

// chainRow reads a chain's row.
func chainStoredRow(t *testing.T, rt Runtime, name string) db.ChainRow {
	t.Helper()
	c, err := rt.Store.Chain(name)
	if err != nil {
		t.Fatalf("Chain %s: %v", name, err)
	}
	return c
}

// chainLog reads a binding's log.
func chainLog(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	return entries
}

// chainTrace reads a chain's trace rows.
func chainTrace(t *testing.T, rt Runtime, name string) []db.ChainEventRow {
	t.Helper()
	events, err := rt.Store.ChainEvents(name)
	if err != nil {
		t.Fatalf("ChainEvents %s: %v", name, err)
	}
	return events
}

// chainPendingChain is the binding's pending KindChain entries: the chain's end
// deliveries that have not been confirmed.
func chainPendingChain(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	var out []store.LogEntry
	for _, e := range chainLog(t, rt, name) {
		if e.Kind == store.KindChain && e.Direction == store.DirToMasterMind && !e.Confirmed {
			out = append(out, e)
		}
	}
	return out
}

// chainReconcile loads a binding and runs one daemon tick under the lock, then
// persists what the tick produced -- the daemon's own read-reconcile-save.
func chainReconcile(t *testing.T, rt Runtime, name string) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		next, err := Reconcile(context.Background(), rt, tx, b)
		if err != nil {
			return err
		}
		out = next
		return tx.Save(next)
	})
	if err != nil {
		t.Fatalf("Reconcile %s: %v", name, err)
	}
	return out
}

// chainBuilderClose closes a writer member's open round: its report and its
// completion marker on disk, then one tick. A writer closes on the marker
// whatever its process is doing.
func chainBuilderClose(t *testing.T, rt Runtime, name string, body string) store.Binding {
	t.Helper()
	b := chainBinding(t, rt, name)
	if err := os.WriteFile(rt.Store.ReportPath(name, b.Round), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s report: %v", name, err)
	}
	touch(t, rt.Store.DonePath(name, b.Round))
	return chainReconcile(t, rt, name)
}

// chainReaderClose closes a reader member's open round: its output file on
// disk, its process observed dead, then one tick. The unmarked-exit path
// closes it with the report, exactly as a reader whose runner wrote its output
// and then exited.
func chainReaderClose(t *testing.T, rt Runtime, name string, body string) store.Binding {
	t.Helper()
	b := chainBinding(t, rt, name)
	path := rt.Store.OutputPath(name, b.Round, bindingRole(b), readerOutputLabel(rt, b))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s output: %v", name, err)
	}
	if fr, ok := rt.Runner.(*fakeRunner); ok && b.Builder.PID != 0 {
		fr.script(b.Builder.PID, false)
	}
	return chainReconcile(t, rt, name)
}

// TestConsumedMemberCloseIsNotPending pins the consumption: a closing chain
// member's report entry is recorded confirmed with the chain's name, and
// nothing is left pending for the mastermind.
func TestConsumedMemberCloseIsNotPending(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	var report *store.LogEntry
	entries := chainLog(t, rt, "shop")
	for i := range entries {
		if entries[i].Kind == store.KindReport && entries[i].Round == 1 {
			report = &entries[i]
		}
	}
	if report == nil {
		t.Fatalf("builder log = %+v, want a report entry for round 1", entries)
	}
	if !report.Confirmed {
		t.Error("a consumed close must be recorded confirmed, not queued")
	}
	if !strings.Contains(report.Note, "consumed by chain shop") {
		t.Errorf("report note = %q, want it to carry consumed by chain shop", report.Note)
	}
	if _, found, err := rt.Store.PendingForMasterMind("shop"); err != nil {
		t.Fatalf("PendingForMasterMind: %v", err)
	} else if found {
		t.Error("a consumed member close must leave nothing pending on the builder")
	}
}

// TestChainBuilderCloseSeedsReviewer pins the wiring's first hop: a builder's
// done close seeds the reviewer's next round, the chain steps to reviewing and
// awaits that round, and one trace row records the state before the event.
func TestChainBuilderCloseSeedsReviewer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	rev := chainBinding(t, rt, "shop-rev")
	if row.Step != string(chain.StepReviewing) {
		t.Errorf("step = %q, want reviewing", row.Step)
	}
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != rev.Round {
		t.Errorf("awaiting = (%s, %d), want (%s, %d)", row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer, rev.Round)
	}
	if row.AwaitingRound == 0 {
		t.Error("Awaiting.Round must never be 0")
	}
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), rev.Round) {
		t.Error("the reviewer's round must be open")
	}

	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want one row", events)
	}
	if events[0].Phase != string(chain.PhaseBuild) || events[0].Step != string(chain.StepBuilding) {
		t.Errorf("trace row = phase %q step %q, want the state before the event (build/building)", events[0].Phase, events[0].Step)
	}
	if events[0].Member != "shop" || events[0].Round != 1 {
		t.Errorf("trace names (%s, %d), want (shop, 1)", events[0].Member, events[0].Round)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != chain.EventBuilderClosed || ev.Gate != chain.GateGreen {
		t.Errorf("event = %+v, want a builder close with a green gate", ev)
	}
}

// TestChainBuilderRedGateSeedsReviewer pins the red gate in this half: a
// failing gate spends nothing and opens no repair round, the event carries red
// and the reviewer is seeded.
func TestChainBuilderRedGateSeedsReviewer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// A gate that has already exited non-zero: the record is scripted rather
	// than run, so the test names only the wiring.
	b := chainBinding(t, rt, "shop")
	b.Gate = "false"
	b.Regate = 2
	b.GateRun = &store.GateRun{PID: 9999, StartedAt: baseTime.Unix(), Round: b.Round, Command: "false"}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatal("the chain runtime must carry a fakeRunner")
	}
	fr.exit(9999, 1)

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepReviewing) {
		t.Errorf("step = %q, want reviewing even on a red gate", row.Step)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), 1) {
		t.Error("the reviewer must be seeded on a red gate")
	}
	// No repair round opened: the builder advanced to round 2 with no prompt
	// for it, and the budget is untouched.
	after := chainBinding(t, rt, "shop")
	if after.Round != 2 {
		t.Errorf("builder round = %d, want 2 (no repair round in this half)", after.Round)
	}
	if HasPromptEntry(chainLog(t, rt, "shop"), 2) {
		t.Error("a chain member must not open a repair round here")
	}
	if after.RepairCount != 0 || after.Regate != 2 {
		t.Errorf("repair budget = %d of %d, want it untouched at 0 of 2", after.RepairCount, after.Regate)
	}

	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want one row", events)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Gate != chain.GateRed {
		t.Errorf("event gate = %q, want red", ev.Gate)
	}
}

// TestChainReviewerPassAdvancesToPlanTwo pins the pass on a multi-plan chain:
// the plan advances, the step returns to building and the builder is seeded
// with the second plan's copy.
func TestChainReviewerPassAdvancesToPlanTwo(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Plans: []string{writePlan(t, "plan one"), writePlan(t, "plan two")}})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	row := chainStoredRow(t, rt, "shop")
	if row.Plan != 2 || row.Step != string(chain.StepBuilding) {
		t.Errorf("chain = plan %d step %q, want plan 2 building", row.Plan, row.Step)
	}
	builder := chainBinding(t, rt, "shop")
	if !HasPromptEntry(chainLog(t, rt, "shop"), builder.Round) {
		t.Errorf("builder round %d must be open after a pass", builder.Round)
	}
	staged, err := os.ReadFile(rt.Store.PromptPath("shop", builder.Round))
	if err != nil {
		t.Fatalf("read the staged plan: %v", err)
	}
	if string(staged) != "plan two" {
		t.Errorf("staged plan = %q, want the second plan's copy", staged)
	}
}

// TestChainReviewerChangesSeedsCorrection pins the changes verdict: the planner
// is seeded with a correction plan and the step is correcting.
func TestChainReviewerChangesSeedsCorrection(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepCorrecting) {
		t.Errorf("step = %q, want correcting", row.Step)
	}
	planner := chainBinding(t, rt, "shop-plan")
	if !HasPromptEntry(chainLog(t, rt, "shop-plan"), planner.Round) {
		t.Error("the planner must be seeded with a correction")
	}
	if row.Plan != 1 {
		t.Errorf("plan = %d, want it held at 1 for a correction", row.Plan)
	}
}

// TestChainCorrectionPlanSendsTheBuilderAndSpendsOneCorrection pins the other
// half: a present correction plan sends the builder and spends exactly one
// correction.
func TestChainCorrectionPlanSendsTheBuilderAndSpendsOneCorrection(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	if row.Corrections != 1 || row.Step != string(chain.StepBuilding) {
		t.Errorf("chain = %d corrections, step %q; want 1 correction, building", row.Corrections, row.Step)
	}
	builder := chainBinding(t, rt, "shop")
	if !HasPromptEntry(chainLog(t, rt, "shop"), builder.Round) {
		t.Errorf("builder round %d must be open after a correction plan", builder.Round)
	}
}

// TestChainSecurityFindingsSeedTheFixPlanner pins the security phase's finding
// branch: a scan that reports findings seeds the planner with a fix plan.
func TestChainSecurityFindingsSeedTheFixPlanner(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Security: ptr(true)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	if row := chainStoredRow(t, rt, "shop"); row.Phase != string(chain.PhaseSecurity) || row.Step != string(chain.StepScanning) {
		t.Fatalf("chain = phase %q step %q, want the security phase scanning", row.Phase, row.Step)
	}

	chainReaderClose(t, rt, "shop-sec", chainFindingsBody(3))

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepPlanningFixes) {
		t.Errorf("step = %q, want planning-fixes", row.Step)
	}
	planner := chainBinding(t, rt, "shop-plan")
	if !HasPromptEntry(chainLog(t, rt, "shop-plan"), planner.Round) {
		t.Error("the planner must be seeded with the fix plan")
	}
}

// TestChainSecurityNoFindingsFinishes pins the clean scan: no findings ends the
// chain.
func TestChainSecurityNoFindingsFinishes(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Security: ptr(true)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	chainReaderClose(t, rt, "shop-sec", chainFindingsBody(0))

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusDone) || row.Phase != string(chain.PhaseFinished) {
		t.Errorf("chain = status %q phase %q, want done/finished", row.Status, row.Phase)
	}
}

// TestChainFinishQueuesExactlyOneDelivery pins the single end delivery:
// finishing queues one on the builder, and a later tick cannot add a second.
func TestChainFinishQueuesExactlyOneDelivery(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Fatalf("chain status = %q, want done", row.Status)
	}
	pending := chainPendingChain(t, rt, "shop")
	if len(pending) != 1 {
		t.Fatalf("pending chain deliveries = %d, want exactly 1", len(pending))
	}
	if pending[0].Round != chainBinding(t, rt, "shop").Round {
		t.Errorf("delivery round = %d, want the builder's current round %d", pending[0].Round, chainBinding(t, rt, "shop").Round)
	}

	// A second tick cannot add another: the terminal transition happened
	// once, so a replayed close for the same member round is ignored.
	rev := chainBinding(t, rt, "shop-rev")
	replay := chain.Event{
		Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 1,
		Verdict: chain.VerdictPass,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return chainApply(context.Background(), rt, tx, rev, replay)
	}); err != nil {
		t.Fatalf("replayed chainApply: %v", err)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries after a second tick = %d, want still 1", len(pending))
	}
}

// TestChainHaltQueuesTheReasonAndResumeCommand pins the halt's end delivery:
// the payload carries why the chain halted and the command that resumes it.
func TestChainHaltQueuesTheReasonAndResumeCommand(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if !strings.Contains(row.Reason, "reviewer gave no verdict") {
		t.Errorf("halt reason = %q, want reviewer gave no verdict", row.Reason)
	}

	pending := chainPendingChain(t, rt, "shop")
	if len(pending) != 1 {
		t.Fatalf("pending chain deliveries = %d, want 1", len(pending))
	}
	payload := pending[0].Payload
	if !strings.Contains(payload, "reviewer gave no verdict") {
		t.Errorf("payload = %q, want it to carry the halt reason", payload)
	}
	if !strings.Contains(payload, "relevo chain --resume --name shop") {
		t.Errorf("payload = %q, want the resume command", payload)
	}
	if !strings.Contains(payload, "relevo show shop --trace") {
		t.Errorf("payload = %q, want the trace command", payload)
	}
}

// TestChainAdvanceWritesStateAndTraceInOneTransaction pins the atomicity of the
// chain's state and its trace row: when the advance cannot be staged, neither
// is written and the old state stands.
func TestChainAdvanceWritesStateAndTraceInOneTransaction(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// Break the stored plan copies, so the reviewer's seed cannot be built:
	// the advance fails before either write.
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.PlanPathsJSON = []byte("{not json")
		return tx.ChainPut(row)
	})
	if err != nil {
		t.Fatalf("plant the staging failure: %v", err)
	}

	b := chainBinding(t, rt, "shop")
	ev := chain.Event{
		Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen,
	}
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		return chainApply(context.Background(), rt, tx, b, ev)
	})
	if err == nil {
		t.Fatal("chainApply = nil, want the staging failure")
	}

	after := chainStoredRow(t, rt, "shop")
	if after.Status != string(chain.StatusRunning) || after.Step != string(chain.StepBuilding) ||
		after.AwaitingMember != chain.MemberBuilder || after.AwaitingRound != 1 {
		t.Errorf("chain row = %+v, want the old running/build/building awaiting the builder", after)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 0 {
		t.Errorf("trace = %+v, want no row when the advance was never written", events)
	}
}

// TestChainIgnoresACloseForAnotherRound pins the awaited-round guard: a close
// that does not name the awaited round changes nothing and writes no trace.
func TestChainIgnoresACloseForAnotherRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	ev := chain.Event{
		Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 2,
		Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return chainApply(context.Background(), rt, tx, b, ev)
	}); err != nil {
		t.Fatalf("chainApply: %v", err)
	}

	after := chainStoredRow(t, rt, "shop")
	if after.Step != string(chain.StepBuilding) || after.AwaitingMember != chain.MemberBuilder || after.AwaitingRound != 1 {
		t.Errorf("chain row = %+v, want it unchanged", after)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 0 {
		t.Errorf("trace = %+v, want no row for a close that did not advance the chain", events)
	}
}

// TestChainReviewerCloseWithoutAVerdictHalts pins the missing verdict: it is a
// halt, never a pass, and the plan does not move.
func TestChainReviewerCloseWithoutAVerdictHalts(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// Put the chain where it awaits the reviewer's first close, so the test
	// names only the verdict.
	row := chainStoredRow(t, rt, "shop")
	row.AwaitingMember = chain.MemberReviewer
	row.AwaitingRound = 1
	row.Step = string(chain.StepReviewing)
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.ChainPut(row) }); err != nil {
		t.Fatalf("seed the awaiting row: %v", err)
	}

	rev := chainBinding(t, rt, "shop-rev")
	ev := chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 1}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return chainApply(context.Background(), rt, tx, rev, ev)
	}); err != nil {
		t.Fatalf("chainApply: %v", err)
	}

	after := chainStoredRow(t, rt, "shop")
	if after.Status != string(chain.StatusHalted) || !strings.Contains(after.Reason, "reviewer gave no verdict") {
		t.Errorf("chain = status %q reason %q, want a halt on the missing verdict", after.Status, after.Reason)
	}
	if after.Plan != 1 {
		t.Errorf("plan = %d, want it held at 1", after.Plan)
	}
}

// TestChainTraceSeqIsPerChain pins that ChainEventAppend allocates the seq per
// chain: two chains each start their own trace at 1.
func TestChainTraceSeqIsPerChain(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Name: "alpha"})
	startedChain(t, rt, ChainOptions{Name: "beta"})

	chainBuilderClose(t, rt, "alpha", chainDoneBody())
	chainBuilderClose(t, rt, "beta", chainDoneBody())

	for _, name := range []string{"alpha", "beta"} {
		events := chainTrace(t, rt, name)
		if len(events) != 1 || events[0].Seq != 1 {
			t.Fatalf("%s trace = %+v, want one row with seq 1", name, events)
		}
	}

	// A second transition on one chain takes the next seq there and nowhere
	// else.
	chainReaderClose(t, rt, "alpha-rev", chainVerdictBody("pass"))
	alpha := chainTrace(t, rt, "alpha")
	if len(alpha) != 2 || alpha[0].Seq != 1 || alpha[1].Seq != 2 {
		t.Errorf("alpha trace = %+v, want seq 1 then 2", alpha)
	}
	if beta := chainTrace(t, rt, "beta"); len(beta) != 1 {
		t.Errorf("beta trace = %+v, want it untouched", beta)
	}
}

// TestSendRefusedOnARunningChainMember pins the refusal on every manual send
// shape: Send, SendDryRun and the under-lock helpers.
func TestSendRefusedOnARunningChainMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	plan := writePlan(t, "x")
	if _, err := Send(context.Background(), rt, "shop", plan, SendOptions{}); !errors.Is(err, ErrRunningChainMember) {
		t.Errorf("Send = %v, want ErrRunningChainMember", err)
	}
	if _, err := SendDryRun(context.Background(), rt, "shop", plan, SendOptions{}); !errors.Is(err, ErrRunningChainMember) {
		t.Errorf("SendDryRun = %v, want ErrRunningChainMember", err)
	}
	// The under-lock checks, the read-only store check and the chain's own
	// member would all refuse through the one wording.
	err := rt.Store.WithLock(func(tx *store.Tx) error { return refuseRunningChainMember(tx, "shop") })
	if !errors.Is(err, ErrRunningChainMember) {
		t.Errorf("refuseRunningChainMember = %v, want ErrRunningChainMember", err)
	}
	if err := refuseRunningChainMemberStore(rt.Store, "shop-rev"); !errors.Is(err, ErrRunningChainMember) {
		t.Errorf("refuseRunningChainMemberStore = %v, want ErrRunningChainMember", err)
	}
	if err := refuseRunningChainMemberStore(rt.Store, "unrelated"); err != nil {
		t.Errorf("a binding that is no chain member = %v, want nil", err)
	}
}

// TestSendAllowedAfterTheChainStops pins the other half of the refusal: once
// the chain has stopped, the member is the mastermind's again.
func TestSendAllowedAfterTheChainStops(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	if _, err := Stop(context.Background(), rt, "shop", StopOptions{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusStopped) {
		t.Fatalf("chain status = %q, want stopped", row.Status)
	}

	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Errorf("Send after the chain stopped = %v, want it allowed", err)
	}
}
