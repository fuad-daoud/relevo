package relevo

import (
	"context"
	"errors"
	"fmt"
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

// chainHaltedBody is a builder's report tail that says the round halted: the
// gate still runs on the done marker, so this is the report the two guards in
// this round have to look past.
func chainHaltedBody(note string) string {
	return "I stopped before finishing.\n\n```relevo\nstatus: halted\nhalted_at: \"" + note + "\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"
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
	if ev.Kind != chain.EventBuilderClosed || ev.Gate != chain.GateNone {
		t.Errorf("event = %+v, want a builder close with no check", ev)
	}
}

// TestChainReviewerSeedSaysNoCheckRan pins the no-check seed: a builder round
// with no gate reaches the reviewer as GateNone, and the staged prompt says no
// check ran instead of naming a result and a log. The correction seed after a
// changes verdict says the same.
func TestChainReviewerSeedSaysNoCheckRan(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want one row", events)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Gate != chain.GateNone {
		t.Fatalf("event gate = %q, want %q", ev.Gate, chain.GateNone)
	}
	rev := chainBinding(t, rt, "shop-rev")
	assertSeedSaysNoCheck(t, rt, "shop-rev", rev.Round)

	// A changes verdict seeds the correction planner; its seed says the same.
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	plan := chainBinding(t, rt, "shop-plan")
	assertSeedSaysNoCheck(t, rt, "shop-plan", plan.Round)
}

// assertSeedSaysNoCheck reads the staged prompt at name's round and pins the
// no-check wording: the exact sentence, no result line, and no gate log path.
func assertSeedSaysNoCheck(t *testing.T, rt Runtime, name string, round int) {
	t.Helper()
	text, err := os.ReadFile(rt.Store.PromptPath(name, round))
	if err != nil {
		t.Fatalf("read %s prompt: %v", name, err)
	}
	if !strings.Contains(string(text), "No check ran for this round.") {
		t.Errorf("%s seed does not say no check ran:\n%s", name, text)
	}
	if strings.Contains(string(text), "Check result:") {
		t.Errorf("%s seed still carries a Check result line:\n%s", name, text)
	}
	if strings.Contains(string(text), rt.Store.GateLogPath("shop", round)) {
		t.Errorf("%s seed names the gate log for a round with no check:\n%s", name, text)
	}
}

// TestChainReviewerSeedNamesTheCheckThatRan pins the gated seed: a passing
// check reaches the reviewer as green with its log, and a red check that spent
// its regate budget reaches it as red with the same log.
func TestChainReviewerSeedNamesTheCheckThatRan(t *testing.T) {
	t.Parallel()

	t.Run("green", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainArmPassingGate(t, rt, "shop", "PASS\n")
		chainBuilderClose(t, rt, "shop", chainDoneBody())

		want := fmt.Sprintf("Check result: green; its output is at %s.", rt.Store.GateLogPath("shop", 1))
		assertSeedNamesCheck(t, rt, "shop-rev", want)
	})

	t.Run("red after regate", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		b := chainBinding(t, rt, "shop")
		b.Regate = 2
		b.RepairCount = 2
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save: %v", err)
		}
		chainArmFailingGate(t, rt, "shop", "FAIL\n")
		chainBuilderClose(t, rt, "shop", chainDoneBody())

		want := fmt.Sprintf("Check result: red; its output is at %s.", rt.Store.GateLogPath("shop", 1))
		assertSeedNamesCheck(t, rt, "shop-rev", want)
	})
}

// assertSeedNamesCheck reads the reviewer's staged prompt and pins the exact
// check line.
func assertSeedNamesCheck(t *testing.T, rt Runtime, name, want string) {
	t.Helper()
	rev := chainBinding(t, rt, name)
	text, err := os.ReadFile(rt.Store.PromptPath(name, rev.Round))
	if err != nil {
		t.Fatalf("read %s prompt: %v", name, err)
	}
	if !strings.Contains(string(text), want) {
		t.Errorf("%s seed does not carry %q:\n%s", name, want, text)
	}
}

// chainArmFailingGate arms a failing gate on the binding's open round: the
// next reconcile finds the run already exited non-zero, so the round closes
// with gate=fail on the first tick. logBody is the gate log the signature
// bound reads; "" leaves it absent, which skips the stall bound.
func chainArmFailingGate(t *testing.T, rt Runtime, name, logBody string) {
	t.Helper()
	b := chainBinding(t, rt, name)
	b.Gate = "false"
	b.GateRun = &store.GateRun{PID: 9999, StartedAt: baseTime.Unix(), Round: b.Round, Command: "false"}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("arm the gate on %s: %v", name, err)
	}
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatal("the chain runtime must carry a fakeRunner")
	}
	fr.exit(9999, 1)
	if logBody != "" {
		if err := os.WriteFile(rt.Store.GateLogPath(name, b.Round), []byte(logBody), 0o644); err != nil {
			t.Fatalf("write the gate log: %v", err)
		}
	}
}

// chainArmPassingGate is chainArmFailingGate's green twin: it arms a gate that
// already exited zero, so the next reconcile closes the round with gate=pass.
// logBody is the gate log the seed names.
func chainArmPassingGate(t *testing.T, rt Runtime, name, logBody string) {
	t.Helper()
	b := chainBinding(t, rt, name)
	b.Gate = "true"
	b.GateRun = &store.GateRun{PID: 9999, StartedAt: baseTime.Unix(), Round: b.Round, Command: "true"}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("arm the passing gate on %s: %v", name, err)
	}
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatal("the chain runtime must carry a fakeRunner")
	}
	fr.exit(9999, 0)
	if logBody != "" {
		if err := os.WriteFile(rt.Store.GateLogPath(name, b.Round), []byte(logBody), 0o644); err != nil {
			t.Fatalf("write the gate log: %v", err)
		}
	}
}

// TestChainBuilderRedGateRepairsBeforeTheReviewer pins the red gate's first
// spend: with a repair still in budget the chain raises no event and writes no
// trace row, the builder's repair round opens in the wiring's later half, and
// the chain waits again on that member.
func TestChainBuilderRedGateRepairsBeforeTheReviewer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	builder := chainBinding(t, rt, "shop")
	row := chainStoredRow(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want the repair round 2", builder.Round)
	}
	if row.Status != string(chain.StatusRunning) || row.Plan != 1 || row.Step != string(chain.StepBuilding) {
		t.Errorf("chain row = %+v, want it running on plan 1 building: a repair is not a transition", row)
	}
	if row.AwaitingMember != chain.MemberBuilder || row.AwaitingRound != builder.Round {
		t.Errorf("awaiting = (%s, %d), want the builder's repair round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberBuilder, builder.Round)
	}
	if builder.RepairCount != 1 || builder.LastGateSig == "" {
		t.Errorf("repair bookkeeping = %d repairs, sig %q; want 1 and a signature", builder.RepairCount, builder.LastGateSig)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop"), builder.Round) {
		t.Error("the repair round's plan must be open on the builder")
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 0 {
		t.Errorf("trace = %+v, want no row: a repair is not a transition", events)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 0 {
		t.Errorf("pending chain deliveries = %+v, want none on a repair", pending)
	}
}

// TestChainBuilderRedAfterRegateSeedsReviewer pins the count bound's other
// half: once the budget is spent, a red gate raises the event and the reviewer
// sees it instead of the member halting.
func TestChainBuilderRedAfterRegateSeedsReviewer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	b.Regate = 2
	b.RepairCount = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	rev := chainBinding(t, rt, "shop-rev")
	if row.Step != string(chain.StepReviewing) {
		t.Errorf("step = %q, want reviewing once the repair budget is spent", row.Step)
	}
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != rev.Round {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer, rev.Round)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), rev.Round) {
		t.Error("the reviewer must be seeded once the repair budget is spent")
	}
	after := chainBinding(t, rt, "shop")
	if after.State == store.StateNeedsYou {
		t.Errorf("builder state = %q, want it left alone: the red event went to the reviewer", after.State)
	}
	if after.RepairCount != 2 {
		t.Errorf("RepairCount = %d, want it held at 2", after.RepairCount)
	}
	if HasPromptEntry(chainLog(t, rt, "shop"), after.Round) {
		t.Error("no repair round may open once the budget is spent")
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

// TestChainBuilderRedWithUnchangedGateOutputSeedsReviewer pins the stall
// bound: the same failure the last repair already saw cannot buy a second
// repair, so the event goes red to the reviewer.
func TestChainBuilderRedWithUnchangedGateOutputSeedsReviewer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	b.Regate = 3
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")

	// The failure equals the one the last repair round already saw, with room
	// left in the budget: only the stall bound can stop the repair.
	b = chainBinding(t, rt, "shop")
	b.RepairCount = 1
	b.LastGateSig = gateSignature(rt.Store.ReadFile, rt.Store.GateLogPath("shop", b.Round))
	if b.LastGateSig == "" {
		t.Fatal("the gate log must hash to a signature")
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	rev := chainBinding(t, rt, "shop-rev")
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != rev.Round {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer, rev.Round)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), rev.Round) {
		t.Error("the reviewer must be seeded when the gate output is unchanged")
	}
	after := chainBinding(t, rt, "shop")
	if after.RepairCount != 1 {
		t.Errorf("RepairCount = %d, want 1: no second repair may open", after.RepairCount)
	}
	if HasPromptEntry(chainLog(t, rt, "shop"), after.Round) {
		t.Error("no second repair round may open on the same failure")
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

// TestChainRepairHaltHaltsTheChain pins the failed start: a repair round that
// cannot open leaves the member NEEDS YOU, and the sweep halts the chain with
// the member's own reason and queues the single end delivery.
func TestChainRepairHaltHaltsTheChain(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A symlinked plan path: the repair round cannot stage its plan, so the
	// member halts instead of opening round 2.
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Symlink(sentinel, rt.Store.PromptPath("shop", 2)); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	builder := chainBinding(t, rt, "shop")
	if builder.State != store.StateNeedsYou {
		t.Fatalf("builder state = %q, want NEEDS YOU: the repair round could not start", builder.State)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Fatalf("chain status = %q, want it still running until the sweep", row.Status)
	}

	tickChains(context.Background(), rt)

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if row.Reason != builder.Halt {
		t.Errorf("halt reason = %q, want the member's reason %q", row.Reason, builder.Halt)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", len(pending))
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 1 {
		t.Errorf("trace = %+v, want the sweep's one row", events)
	}
}

// TestChainRepairRoundCloseReachesTheReviewer pins the repair round as a
// member round: its own close is the next event the chain maps, and a green
// gate there seeds the reviewer.
func TestChainRepairRoundCloseReachesTheReviewer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	if row := chainStoredRow(t, rt, "shop"); row.AwaitingMember != chain.MemberBuilder {
		t.Fatalf("awaiting = %q, want the builder's repair round", row.AwaitingMember)
	}

	// The repair round passes: with no gate the close is green, and the chain
	// maps it like any other builder round.
	b = chainBinding(t, rt, "shop")
	b.Gate = ""
	b.GateRun = nil
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	rev := chainBinding(t, rt, "shop-rev")
	if row.Step != string(chain.StepReviewing) {
		t.Errorf("step = %q, want reviewing after the repair round closed", row.Step)
	}
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != rev.Round {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer, rev.Round)
	}
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), rev.Round) {
		t.Error("the reviewer must be seeded for the repair round's close")
	}
	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want one row for the repair round's close", events)
	}
	if events[0].Round != 2 {
		t.Errorf("trace round = %d, want the repair round 2", events[0].Round)
	}
}

// TestNonMemberRepairUnchanged pins that a binding no chain owns keeps the
// gate repair loop exactly as it was: the budget opens a repair round, and a
// spent budget halts the member.
func TestNonMemberRepairUnchanged(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, _ := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n")
	if got.Round != 2 || got.RepairCount != 1 || got.State != store.StateActive {
		t.Fatalf("after the first failure: round=%d repairs=%d state=%q, want 2/1/active",
			got.Round, got.RepairCount, got.State)
	}
	if !anySpecArgv(fr, "002-prompt.md") {
		t.Error("a non-member must still be handed its repair round")
	}

	// A different failure spends the last of the budget: the member halts.
	got, _ = failRoundWithGate(t, rt, got, fr, "FAIL github.com/example/other-pkg\n")
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %q, want needs_you once the budget is spent", got.State)
	}
	if !strings.Contains(got.Halt, "after 1 repair") {
		t.Errorf("halt = %q, want the count bound's wording", got.Halt)
	}
}

// TestChainAwaitsTheSentMembersOwnRound pins the round the chain stores when
// it sends: it is the member's own new round, never the round the chain held
// from the close before. In a real chain the members' rounds diverge, and a
// stale round would make the chain ignore that member's close forever.
func TestChainAwaitsTheSentMembersOwnRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// The reviewer is rounds ahead of the builder's first: its next send opens
	// round 4, which the builder's stale round 1 must not shadow.
	rev := chainBinding(t, rt, "shop-rev")
	rev.Round = 4
	if err := rt.Store.Save(rev); err != nil {
		t.Fatalf("Save: %v", err)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	rev = chainBinding(t, rt, "shop-rev")
	if rev.Round != 4 {
		t.Fatalf("reviewer round = %d, want 4", rev.Round)
	}
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != 4 {
		t.Fatalf("awaiting = (%s, %d), want the reviewer's own round (%s, 4)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer)
	}

	// The close of that round advances the chain; a stale awaiting round would
	// have dropped it. A pass on the only plan finishes the chain.
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Errorf("chain status = %q, want done: the reviewer's own round must advance the chain", row.Status)
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
		return chainApply(context.Background(), rt, tx, rev, replay, nil)
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
		return chainApply(context.Background(), rt, tx, b, ev, nil)
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
		return chainApply(context.Background(), rt, tx, b, ev, nil)
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
		return chainApply(context.Background(), rt, tx, rev, ev, nil)
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

// TestChainBuilderHaltedWithRedGateHaltsTheChain pins the gate-does-not-decide
// rule: the gate runs on the done marker whatever the report says, so a builder
// that reports `halted` with a failing gate and a repair still in budget halts
// the chain -- it never opens a repair round, and the chain's one end delivery
// is queued. Removing either guard (chainApply's outcome guard, or markerClose's
// chain-member guard) fails this test.
func TestChainBuilderHaltedWithRedGateHaltsTheChain(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// A repair is still in budget, and the gate fails: without the outcome
	// guard this is exactly the repair path's input.
	b := chainBinding(t, rt, "shop")
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")

	chainBuilderClose(t, rt, "shop", chainHaltedBody("waiting on a decision"))

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted: a halted builder never repairs", row.Status)
	}
	if !strings.HasPrefix(row.Reason, "builder halted on plan 1:") {
		t.Errorf("halt reason = %q, want the builder-halted wording on plan 1", row.Reason)
	}

	builder := chainBinding(t, rt, "shop")
	if HasPromptEntry(chainLog(t, rt, "shop"), builder.Round) {
		t.Errorf("a round %d prompt entry exists; a halted report must open no repair round", builder.Round)
	}
	if builder.RepairCount != 0 {
		t.Errorf("RepairCount = %d, want 0: no repair round may open", builder.RepairCount)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 1 {
		t.Errorf("trace = %+v, want the halt's one row", events)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", len(pending))
	}
}

// TestChainHaltWithTheBuilderGoneDeliversOnASurvivingMember pins the end
// delivery's carrier: when the record that is gone is the builder's, the
// MasterMind is still told -- the delivery is queued on the first member whose
// record exists, reviewer before planner before security.
func TestChainHaltWithTheBuilderGoneDeliversOnASurvivingMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// The builder's record is the one that is gone; the reviewer's survives.
	if err := rt.Store.Delete("shop"); err != nil {
		t.Fatalf("Delete shop: %v", err)
	}

	tickChains(context.Background(), rt)

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if !strings.Contains(row.Reason, "member shop gone") {
		t.Errorf("halt reason = %q, want the missing member named", row.Reason)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 0 {
		t.Errorf("pending on the gone builder = %d, want none", len(pending))
	}
	pending := chainPendingChain(t, rt, "shop-rev")
	if len(pending) != 1 {
		t.Fatalf("pending chain deliveries on shop-rev = %d, want the one end delivery", len(pending))
	}
	if pending[0].Kind != store.KindChain || pending[0].Direction != store.DirToMasterMind {
		t.Errorf("delivery = %+v, want a mastermind-bound chain entry", pending[0])
	}
	if !strings.Contains(pending[0].Payload, "chain shop halted") {
		t.Errorf("payload = %q, want the chain's end payload", pending[0].Payload)
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
