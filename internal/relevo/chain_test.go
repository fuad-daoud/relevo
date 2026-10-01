package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
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

// TestConsumedMemberCloseReadsAsSeen pins the status side of the consumption: a
// report a running chain took must not keep painting REPORT IN on the member
// row -- the chain's own end delivery is the mastermind's copy of it.
func TestConsumedMemberCloseReadsAsSeen(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row, err := statusRow(context.Background(), rt, chainBinding(t, rt, "shop"))
	if err != nil {
		t.Fatalf("statusRow: %v", err)
	}
	if row.Unread {
		t.Error("a consumed report must read as seen")
	}
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

		want := fmt.Sprintf("Check result: green; its output: %s.", rt.Store.GateLogPath("shop", 1))
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

		want := fmt.Sprintf("Check result: red; its output: %s.", rt.Store.GateLogPath("shop", 1))
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
		_, err := chainApply(context.Background(), rt, tx, rev, replay, nil, nil)
		return err
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
		_, err := chainApply(context.Background(), rt, tx, b, ev, nil, nil)
		return err
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
		_, err := chainApply(context.Background(), rt, tx, b, ev, nil, nil)
		return err
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
		_, err := chainApply(context.Background(), rt, tx, rev, ev, nil, nil)
		return err
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

// chainSourceBody is a builder report tail with only the fields a
// BuilderHaltReason source test varies. An empty field is left empty rather
// than quoted, so the source is genuinely absent.
func chainSourceBody(status, haltedAt, notDone string) string {
	body := "I stopped.\n\n```relevo\nstatus: " + status + "\n"
	if haltedAt != "" {
		body += "halted_at: \"" + haltedAt + "\"\n"
	} else {
		body += "halted_at: \"\"\n"
	}
	body += "changed_paths: []\ncommands_run: []\nnot_done: [" + notDone + "]\n```\n"
	return body
}

// TestChainBuilderHaltCarriesTheReportTailReason pins the whole halt path: a
// builder body whose tail names where it stopped reaches the chain row, the
// trace row, the encoded event and the one end delivery with the labelled
// reason, not the empty note the wiring used to format.
func TestChainBuilderHaltCarriesTheReportTailReason(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainHaltedBody("waiting on a decision"))

	want := "builder halted on plan 1: halted_at: waiting on a decision"
	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if row.Reason != want {
		t.Errorf("chain reason = %q, want %q", row.Reason, want)
	}

	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want the halt's one row", events)
	}
	if events[0].Reason != want {
		t.Errorf("trace reason = %q, want %q", events[0].Reason, want)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Reason != "halted_at: waiting on a decision" {
		t.Errorf("event reason = %q, want the report tail's source", ev.Reason)
	}

	pending := chainPendingChain(t, rt, "shop")
	if len(pending) != 1 {
		t.Fatalf("pending chain deliveries = %d, want 1", len(pending))
	}
	if !strings.Contains(pending[0].Payload, want) {
		t.Errorf("end payload = %q, want it to carry %q", pending[0].Payload, want)
	}
}

// TestChainBuilderHaltReasonSources pins the three remaining sources end to
// end: the close note, the first not_done item, and the outcome word when the
// tail is empty.
func TestChainBuilderHaltReasonSources(t *testing.T) {
	t.Parallel()

	t.Run("the note", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		// A marker with no report closes with the note "noreport": the tail
		// parses to nothing, so the note is the only source left.
		b := chainBinding(t, rt, "shop")
		touch(t, rt.Store.DonePath("shop", b.Round))
		chainReconcile(t, rt, "shop")

		if got := chainStoredRow(t, rt, "shop").Reason; got != "builder halted on plan 1: note: noreport" {
			t.Errorf("chain reason = %q, want the noreport note", got)
		}
	})

	t.Run("the first not_done item", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		chainBuilderClose(t, rt, "shop", chainSourceBody("halted", "", "finish the tests, then the docs"))

		if got := chainStoredRow(t, rt, "shop").Reason; got != "builder halted on plan 1: not_done: finish the tests" {
			t.Errorf("chain reason = %q, want the first not_done item", got)
		}
	})

	t.Run("the outcome when the tail is empty", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		chainBuilderClose(t, rt, "shop", chainSourceBody("blocked", "", ""))

		if got := chainStoredRow(t, rt, "shop").Reason; got != "builder halted on plan 1: status: blocked" {
			t.Errorf("chain reason = %q, want the outcome word", got)
		}
	})
}

// chainPlanDiffFixtures arms the fake git so a round close produces both a
// round patch and a cumulative plan patch.
func chainPlanDiffFixtures(fg *fakeGit) {
	fg.snapshotTreeID = "tree-end"
	fg.diffResult = git.Diff{
		Stat:  git.Stat{FilesChanged: 2, Insertions: 5, Deletions: 1},
		Patch: []byte("--- a/f\n+++ b/f\n"),
	}
}

// TestChainReviewerSeedNamesThePlanCumulativeDiff pins the seed's cumulative
// line: with a plan-start commit and a closing round whose tree is known, the
// staged reviewer prompt names the copies of this round's diff and the plan's
// whole diff -- never the round_file keys themselves -- and each copy holds the
// key's bytes.
func TestChainReviewerSeedNamesThePlanCumulativeDiff(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	diffKey := rt.Store.DiffPath("shop", 1)
	planDiffKey := rt.Store.PlanDiffPath("shop", 1)
	diffCopy, ok := rt.Store.ChainInputPath("shop", diffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", diffKey)
	}
	planDiffCopy, ok := rt.Store.ChainInputPath("shop", planDiffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", planDiffKey)
	}

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	if got, want := string(text), "This round's diff: "+diffCopy+"."; !strings.Contains(got, want) {
		t.Errorf("reviewer seed does not name the diff copy %q:\n%s", want, got)
	}
	if got, want := string(text), "Plan diff, every round of this plan so far: "+planDiffCopy+"."; !strings.Contains(got, want) {
		t.Errorf("reviewer seed does not name the plan-diff copy %q:\n%s", want, got)
	}
	if got := string(text); strings.Contains(got, diffKey) || strings.Contains(got, planDiffKey) {
		t.Errorf("reviewer seed names a round_file key:\n%s", got)
	}

	patch, err := rt.Store.ReadFile(planDiffKey)
	if err != nil {
		t.Fatalf("read %s: %v", planDiffKey, err)
	}
	if string(patch) != "--- a/f\n+++ b/f\n" {
		t.Errorf("plan diff patch = %q, want the diff's patch", patch)
	}
	copied, err := rt.Store.ReadFile(planDiffCopy)
	if err != nil {
		t.Fatalf("read the plan-diff copy %s: %v", planDiffCopy, err)
	}
	if string(copied) != string(patch) {
		t.Errorf("plan-diff copy = %q, want the key's bytes %q", copied, patch)
	}
}

// TestChainPlanDiffStartsAtThePlanStartCommit pins the span's start: a
// two-plan chain whose head moves between sends diffs plan 2's review from the
// head recorded at plan 2's send, not from the plan's closing round.
func TestChainPlanDiffStartsAtThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{Plans: []string{writePlan(t, "plan one"), writePlan(t, "plan two")}})

	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "commit-head-123" {
		t.Fatalf("plan 1 start = %q, want the cut commit", got)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	// The head moves before plan 2's send, so a review that diffed from the
	// closing round's baseline would name a different start.
	fg.headCommitID = "head-plan2"
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	row := chainStoredRow(t, rt, "shop")
	if row.Plan != 2 {
		t.Fatalf("chain plan = %d, want 2", row.Plan)
	}
	if row.PlanStartCommit != "head-plan2" {
		t.Fatalf("plan 2 start = %q, want the head recorded at plan 2's send", row.PlanStartCommit)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	if fg.lastDiffFrom != "head-plan2" {
		t.Errorf("the cumulative diff ran from %q, want plan 2's start %q", fg.lastDiffFrom, "head-plan2")
	}
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-plan2" {
		t.Errorf("plan start after the plan's review = %q, want it held at %q", got, "head-plan2")
	}
}

// TestChainPlanStartCommitResetOnlyOnNewPlans pins which sends record the plan
// start: a correction keeps it, a reviewer's pass onto the next plan moves it
// to that send's baseline head, and a security fix-plan send moves it too.
func TestChainPlanStartCommitResetOnlyOnNewPlans(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{
		Plans:          []string{writePlan(t, "plan one"), writePlan(t, "plan two")},
		MaxCorrections: ptr(2),
		Security:       ptr(true),
	})

	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "commit-head-123" {
		t.Fatalf("plan 1 start = %q, want the cut commit", got)
	}

	// A correction is sent by the planner's close. It must leave the recorded
	// start alone even though the head has moved.
	fg.headCommitID = "head-correction"
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "commit-head-123" {
		t.Fatalf("plan start after a correction = %q, want it held at the plan 1 start", got)
	}

	// The reviewer's pass onto plan 2 moves it to that send's baseline head.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	fg.headCommitID = "head-plan2"
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-plan2" {
		t.Fatalf("plan start after the pass = %q, want plan 2's head", got)
	}

	// A reviewer send into the security phase changes nothing.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-plan2" {
		t.Fatalf("plan start after the security send = %q, want plan 2's head", got)
	}

	// The security phase's fix plan moves it to that send's baseline head.
	chainReaderClose(t, rt, "shop-sec", chainFindingsBody(1))
	fg.headCommitID = "head-fix"
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != "head-fix" {
		t.Errorf("plan start after the fix plan = %q, want %q", got, "head-fix")
	}
}

// TestChainCorrectionRoundRunsThePlannersPlan pins the correction delivery: a
// correction planner's own plan is what the builder's next round is handed, not
// the chain's copy of the plan.
func TestChainCorrectionRoundRunsThePlannersPlan(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	const planText = "# Correction plan\n\n1. Make the change the reviewer asked for.\n"
	chainReaderClose(t, rt, "shop-plan", planText)

	builder := chainBinding(t, rt, "shop")
	staged, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", builder.Round))
	if err != nil {
		t.Fatalf("read the builder's staged prompt: %v", err)
	}
	if string(staged) != planText {
		t.Errorf("builder round %d prompt = %q, want the planner's own plan %q", builder.Round, staged, planText)
	}
	copied, err := rt.Store.ReadFile(rt.Store.ChainPlanPath("shop", 1))
	if err != nil {
		t.Fatalf("read the chain's plan copy: %v", err)
	}
	if string(staged) == string(copied) {
		t.Errorf("the builder was handed the plan copy %q, not the planner's plan", copied)
	}
}

// TestChainReviewerSeedNamesTheRoundPromptOfACorrection pins the round prompt:
// a reviewer seed after a round that ran a planner's plan names that round's
// staged prompt.
func TestChainReviewerSeedNamesTheRoundPromptOfACorrection(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", "# Correction plan\n\nDo it.\n")
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	if want := "This round's prompt: " + rt.Store.PromptPath("shop", 2); !strings.Contains(string(text), want) {
		t.Errorf("reviewer seed does not name %q:\n%s", want, text)
	}
}

// TestChainReviewerSeedOmitsThePlanCopyAsRoundPrompt pins the plan's own round:
// when the round ran the plan copy itself, the seed names no round prompt.
func TestChainReviewerSeedOmitsThePlanCopyAsRoundPrompt(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	if strings.Contains(string(text), "This round's prompt:") {
		t.Errorf("plan 1's own round names a round prompt:\n%s", text)
	}
}

// TestChainRemoteRedGateStagesARepairRound pins the chain's own repair for a
// remote member: a red close with budget left raises no event and writes no
// trace row, the chain stages the repair text on the client's member record --
// the served binding never opens one -- and the pending-send step ships it with
// verify off. The staged prompt's entry note is the chain step note, not a
// local repair's `repair k/M`.
func TestChainRemoteRedGateStagesARepairRound(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, fg, _ := chainRemoteRuntime(t, fr)
	fg.refSHA = map[string]string{"refs/heads/relevo/shop": "repair-head"}
	seedRemoteChain(t, rt, "shop", remoteChainOpts{Regate: 2})

	remoteChainClose(t, rt, fr, "shop", remote.BindingView{
		ResultCommit: "result-r1", GateResult: "fail", ReportOutcome: "done",
		DiffNote: "1 file changed",
	}, map[string]string{"report": chainDoneBody(), "diff": "diff body\n", "gate": "FAIL the thing\n"})

	b := chainBinding(t, rt, "shop")
	row := chainStoredRow(t, rt, "shop")
	if b.Round != 2 {
		t.Fatalf("builder round = %d, want the staged repair round 2", b.Round)
	}
	if b.RepairCount != 1 || b.LastGateSig == "" {
		t.Errorf("repair bookkeeping = %d repairs, sig %q; want 1 and a signature", b.RepairCount, b.LastGateSig)
	}
	if b.RoundBaselineHead != "repair-head" {
		t.Errorf("RoundBaselineHead = %q, want the branch head the staging read", b.RoundBaselineHead)
	}
	if b.RoundClosedTree != "" {
		t.Errorf("RoundClosedTree = %q, want it cleared for the new round", b.RoundClosedTree)
	}
	if row.Status != string(chain.StatusRunning) || row.Step != string(chain.StepBuilding) {
		t.Errorf("chain row = %+v, want it running/building: a repair is not a transition", row)
	}
	if row.AwaitingMember != chain.MemberBuilder || row.AwaitingRound != 2 {
		t.Errorf("awaiting = (%s, %d), want the builder's repair round (builder, 2)", row.AwaitingMember, row.AwaitingRound)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 0 {
		t.Errorf("trace = %+v, want no row: a repair is not a transition", events)
	}
	if HasPromptEntry(chainLog(t, rt, "shop"), 2) {
		t.Fatal("no prompt entry may exist before the pending-send step ships")
	}

	staged, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", 2))
	if err != nil {
		t.Fatalf("read the staged repair: %v", err)
	}
	if !strings.Contains(string(staged), "round 1's gate failed") {
		t.Errorf("staged repair does not name the failed round:\n%s", staged)
	}
	if !strings.Contains(string(staged), "FAIL the thing") {
		t.Errorf("staged repair does not carry the gate tail:\n%s", staged)
	}

	fr.calls = nil
	if err := chainSendPending(context.Background(), rt); err != nil {
		t.Fatalf("chainSendPending: %v", err)
	}
	if string(fr.startRoundPlan) != string(staged) {
		t.Errorf("shipped plan = %q, want the staged repair text", fr.startRoundPlan)
	}
	if fr.startRoundVerify == nil || *fr.startRoundVerify {
		t.Errorf("verify = %v, want an explicit false", fr.startRoundVerify)
	}
	if !promptNoteFor(chainLog(t, rt, "shop"), 2, "chain builder") {
		t.Errorf("prompt log = %+v, want the chain step note on the repair's entry", chainLog(t, rt, "shop"))
	}
}

// TestChainRemoteRedGateAfterTheBudgetReachesTheReviewer pins the count bound's
// other half for a remote member: once the budget is spent the event goes red
// to the reviewer, which the seed names as a red check with its log, and the
// trace records the builder_closed row.
func TestChainRemoteRedGateAfterTheBudgetReachesTheReviewer(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	seedRemoteChain(t, rt, "shop", remoteChainOpts{Regate: 2})
	b := chainBinding(t, rt, "shop")
	b.RepairCount = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	remoteChainClose(t, rt, fr, "shop", remote.BindingView{
		ResultCommit: "result-r1", GateResult: "fail", ReportOutcome: "done",
		DiffNote: "1 file changed",
	}, map[string]string{"report": chainDoneBody(), "diff": "diff body\n", "gate": "FAIL the thing\n"})

	row := chainStoredRow(t, rt, "shop")
	rev := chainBinding(t, rt, "shop-rev")
	if row.Step != string(chain.StepReviewing) {
		t.Errorf("step = %q, want reviewing once the repair budget is spent", row.Step)
	}
	if row.AwaitingMember != chain.MemberReviewer || row.AwaitingRound != rev.Round {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (%s, %d)",
			row.AwaitingMember, row.AwaitingRound, chain.MemberReviewer, rev.Round)
	}
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer seed: %v", err)
	}
	// The seed names a red check and a gate log a runner can open: the log's
	// own path when the store reads it as a regular file, a copy otherwise.
	// Read the file the seed names rather than a fixed path, and hold its bytes
	// to the gate log the store keeps.
	const gateLine = "Check result: red; its output: "
	seed := string(text)
	i := strings.Index(seed, gateLine)
	if i < 0 {
		t.Fatalf("reviewer seed does not carry the red check line:\n%s", seed)
	}
	namedGate := strings.TrimSuffix(strings.SplitN(seed[i+len(gateLine):], "\n", 2)[0], ".")
	if !rt.Store.DiskRegularFile(namedGate) {
		t.Errorf("the seed's red gate log %s is not a regular file on disk", namedGate)
	}
	gotGate, err := rt.Store.ReadFile(namedGate)
	if err != nil {
		t.Fatalf("read the seed's red gate log %s: %v", namedGate, err)
	}
	wantGate, err := rt.Store.ReadFile(rt.Store.GateLogPath("shop", 1))
	if err != nil {
		t.Fatalf("read the gate log: %v", err)
	}
	if string(gotGate) != string(wantGate) {
		t.Errorf("the seed's red gate log %s = %q, want the gate log's %q", namedGate, gotGate, wantGate)
	}
	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want one row", events)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != chain.EventBuilderClosed || ev.Gate != chain.GateRed {
		t.Errorf("event = %+v, want a red builder close", ev)
	}
}

// TestRemoteBuilderCloseSeedsTheReviewerWithThePulledRound pins the whole
// remote close to seed path: the reviewer's staged prompt names the builder
// round's report, diff, gate log, plan diff and round prompt, every named file
// exists, and the plan diff ran from the recorded plan-start commit to the
// pulled result commit.
func TestRemoteBuilderCloseSeedsTheReviewerWithThePulledRound(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, fg, _ := chainRemoteRuntime(t, fr)
	seedRemoteChain(t, rt, "shop", remoteChainOpts{Regate: 2})
	fg.diffResult = git.Diff{Stat: git.Stat{FilesChanged: 1}, Patch: []byte("plan diff patch\n")}

	remoteChainClose(t, rt, fr, "shop", remote.BindingView{
		ResultCommit: "result-r1", GateResult: "pass", ReportOutcome: "done",
		DiffNote: "1 file changed",
	}, map[string]string{"report": chainDoneBody(), "diff": "diff body\n", "gate": "ok\n"})

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer seed: %v", err)
	}
	seed := string(text)
	for _, key := range []string{
		rt.Store.ReportPath("shop", 1),
		rt.Store.DiffPath("shop", 1),
		rt.Store.GateLogPath("shop", 1),
		rt.Store.PlanDiffPath("shop", 1),
		rt.Store.PromptPath("shop", 1),
	} {
		// The seed names the input's own path when the store reads it as a
		// regular file, and a copy under the chain's directory otherwise; either
		// way the named path exists on disk and holds what the store holds for
		// the original key.
		named := key
		if !rt.Store.DiskRegularFile(named) {
			copy, ok := rt.Store.ChainInputPath("shop", key)
			if !ok {
				t.Fatalf("ChainInputPath(%s) = false", key)
			}
			named = copy
		}
		if !rt.Store.DiskRegularFile(named) {
			t.Errorf("reviewer seed's input %s is not a regular file on disk", named)
		}
		if !strings.Contains(seed, named) {
			t.Errorf("reviewer seed does not name %s:\n%s", named, seed)
		}
		got, err := rt.Store.ReadFile(named)
		if err != nil {
			t.Errorf("read the seed's named file %s: %v", named, err)
			continue
		}
		want, err := rt.Store.ReadFile(key)
		if err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		if string(got) != string(want) {
			t.Errorf("the seed's named file %s = %q, want the store's %q", named, got, want)
		}
	}
	if fg.lastDiffFrom != seedRemoteBase || fg.lastDiffTo != "result-r1" {
		t.Errorf("plan diff ran from %q to %q, want %q to the pulled result commit",
			fg.lastDiffFrom, fg.lastDiffTo, seedRemoteBase)
	}
}

// TestChainRemoteBuilderAdvanceStagesPlanTwo pins the later plan: after the
// reviewer passes plan 1, the builder's next round is staged at the builder's
// prompt path rather than started locally, and the chain records the branch
// head the staging read as plan 2's start commit.
func TestChainRemoteBuilderAdvanceStagesPlanTwo(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, fg, _ := chainRemoteRuntime(t, fr)
	fg.refSHA = map[string]string{"refs/heads/relevo/shop": "plan-two-head"}
	seedRemoteChain(t, rt, "shop", remoteChainOpts{Plans: 2})

	remoteChainClose(t, rt, fr, "shop", remote.BindingView{
		ResultCommit: "result-r1", ReportOutcome: "done", DiffNote: "1 file changed",
	}, map[string]string{"report": chainDoneBody(), "diff": "diff body\n"})

	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	row := chainStoredRow(t, rt, "shop")
	if row.Plan != 2 {
		t.Errorf("plan = %d, want 2", row.Plan)
	}
	if row.PlanStartCommit != "plan-two-head" {
		t.Errorf("plan-start commit = %q, want the branch head the staging read", row.PlanStartCommit)
	}
	if row.AwaitingMember != chain.MemberBuilder || row.AwaitingRound != 2 {
		t.Errorf("awaiting = (%s, %d), want the builder's plan-2 round (builder, 2)", row.AwaitingMember, row.AwaitingRound)
	}
	staged, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", 2))
	if err != nil {
		t.Fatalf("read the staged plan 2: %v", err)
	}
	if string(staged) != "plan 2\n" {
		t.Errorf("staged plan 2 = %q, want the chain's plan-2 copy", staged)
	}
	if HasPromptEntry(chainLog(t, rt, "shop"), 2) {
		t.Error("a remote member's round is staged, not started: no prompt entry may exist yet")
	}
}

// TestChainRemoteCorrectionShipsThePlannerPlan pins the planner's plan reaching
// a remote builder: the chain stages the planner's artifact byte-for-byte at
// the builder's next round, and the pending-send step ships exactly that text
// with the chain step note.
func TestChainRemoteCorrectionShipsThePlannerPlan(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	seedRemoteChain(t, rt, "shop", remoteChainOpts{})

	remoteChainClose(t, rt, fr, "shop", remote.BindingView{
		ResultCommit: "result-r1", ReportOutcome: "done", DiffNote: "1 file changed",
	}, map[string]string{"report": chainDoneBody(), "diff": "diff body\n"})

	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	const plannerPlan = "correction plan: change the one thing\n"
	chainReaderClose(t, rt, "shop-plan", plannerPlan)

	staged, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", 2))
	if err != nil {
		t.Fatalf("read the staged correction: %v", err)
	}
	if string(staged) != plannerPlan {
		t.Errorf("staged builder prompt = %q, want the planner's plan byte-for-byte", staged)
	}

	fr.calls = nil
	if err := chainSendPending(context.Background(), rt); err != nil {
		t.Fatalf("chainSendPending: %v", err)
	}
	if string(fr.startRoundPlan) != plannerPlan {
		t.Errorf("shipped plan = %q, want the planner's plan", fr.startRoundPlan)
	}
	if fr.startRoundVerify == nil || *fr.startRoundVerify {
		t.Errorf("verify = %v, want an explicit false", fr.startRoundVerify)
	}
	if !promptNoteFor(chainLog(t, rt, "shop"), 2, "chain builder") {
		t.Errorf("prompt log = %+v, want the chain step note", chainLog(t, rt, "shop"))
	}
}

// TestChainTraceRowsKeepTheirOwnPlan pins the row's own plan: a chain that has
// advanced onto plan 2 still renders its plan-1 rows as plan 1/N, because each
// chain_event row stores the plan it was written on.
func TestChainTraceRowsKeepTheirOwnPlan(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Plans: []string{writePlan(t, "plan one"), writePlan(t, "plan two")}})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	if row := chainStoredRow(t, rt, "shop"); row.Plan != 2 {
		t.Fatalf("chain plan = %d, want 2", row.Plan)
	}
	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if len(doc.Events) != 2 {
		t.Fatalf("trace = %+v, want two rows", doc.Events)
	}
	for _, line := range strings.Split(strings.TrimRight(RenderTrace(doc), "\n"), "\n") {
		if !strings.HasPrefix(line, "plan 1/2  ") {
			t.Errorf("line %q does not start with plan 1/2: a row must keep the plan it was written on", line)
		}
	}
}

// TestChainTraceRowWithoutAPlanUsesTheChainsPlan pins the pre-019 fallback: a
// row stored with plan 0 -- what a row written before the column existed reads
// -- renders the chain's current plan rather than plan 0.
func TestChainTraceRowWithoutAPlanUsesTheChainsPlan(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Plans: []string{writePlan(t, "plan one"), writePlan(t, "plan two")}})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	if row := chainStoredRow(t, rt, "shop"); row.Plan != 2 {
		t.Fatalf("chain plan = %d, want 2", row.Plan)
	}

	// A row written before 019: its stored plan reads 0, and the trace shows
	// the chain's current plan.
	ev := chain.Event{
		Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 3,
		Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainEventAppend("shop", db.ChainEventRow{
			Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
			Member: "shop", Round: 3, Plan: 0,
			Event: ev.Encode(), Action: chain.Action{Kind: chain.ActionSend}.Encode(),
		})
	}); err != nil {
		t.Fatalf("plant the plan-0 row: %v", err)
	}

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	lines := strings.Split(strings.TrimRight(RenderTrace(doc), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "plan 2/2  ") {
		t.Errorf("plan-0 row rendered %q, want the chain's current plan 2/2", last)
	}
}

// TestChainReviewerSeedNamesACopyOfTheSealedDiff pins the seed's diff input: the
// reviewer seed names an existing regular file under the chain's directory whose
// bytes are the round diff, never the row-only diff key a runner cannot open.
func TestChainReviewerSeedNamesACopyOfTheSealedDiff(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	key := rt.Store.DiffPath("shop", 1)
	body, err := rt.Store.ReadFile(key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	copy, ok := rt.Store.ChainInputPath("shop", key)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", key)
	}
	if !rt.Store.DiskRegularFile(copy) {
		t.Fatalf("the seed's diff copy %s is not a regular file on disk", copy)
	}
	if !strings.Contains(string(text), "This round's diff: "+copy+".") {
		t.Errorf("reviewer seed does not name the diff copy %s:\n%s", copy, text)
	}
	if strings.Contains(string(text), key) {
		t.Errorf("reviewer seed names the round_file key %s:\n%s", key, text)
	}
	copied, err := rt.Store.ReadFile(copy)
	if err != nil {
		t.Fatalf("read the copy %s: %v", copy, err)
	}
	if string(copied) != string(body) {
		t.Errorf("copy bytes = %q, want the diff's %q", copied, body)
	}
}

// TestChainReviewerSeedNamesACopyOfThePlanDiff is the cumulative line's twin of
// the round diff: the plan-diff key is copied, and the copy holds its bytes.
func TestChainReviewerSeedNamesACopyOfThePlanDiff(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	key := rt.Store.PlanDiffPath("shop", 1)
	body, err := rt.Store.ReadFile(key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	copy, ok := rt.Store.ChainInputPath("shop", key)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", key)
	}
	if !rt.Store.DiskRegularFile(copy) {
		t.Fatalf("the seed's plan-diff copy %s is not a regular file on disk", copy)
	}
	if !strings.Contains(string(text), "Plan diff, every round of this plan so far: "+copy+".") {
		t.Errorf("reviewer seed does not name the plan-diff copy %s:\n%s", copy, text)
	}
	if strings.Contains(string(text), key) {
		t.Errorf("reviewer seed names the round_file key %s:\n%s", key, text)
	}
	copied, err := rt.Store.ReadFile(copy)
	if err != nil {
		t.Fatalf("read the copy %s: %v", copy, err)
	}
	if string(copied) != string(body) {
		t.Errorf("copy bytes = %q, want the plan diff's %q", copied, body)
	}
}

// TestChainCorrectionSeedCopiesASealedBuilderReport pins the sealed case: after
// a seal pass moves the judged builder round's report into round_file and
// removes it from disk, the correction seed still names a copy holding its
// bytes.
func TestChainCorrectionSeedCopiesASealedBuilderReport(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	// Seal the builder's judged round: its report is now a row, not a file.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := tx.SealRound("shop", 1)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	reportKey := rt.Store.ReportPath("shop", 1)
	if rt.Store.DiskRegularFile(reportKey) {
		t.Fatalf("the builder's report %s is still on disk after the seal", reportKey)
	}
	body, err := rt.Store.ReadFile(reportKey)
	if err != nil {
		t.Fatalf("read the sealed report %s: %v", reportKey, err)
	}

	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	planner := chainBinding(t, rt, "shop-plan")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-plan", planner.Round))
	if err != nil {
		t.Fatalf("read the correction prompt: %v", err)
	}
	copy, ok := rt.Store.ChainInputPath("shop", reportKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", reportKey)
	}
	if !strings.Contains(string(text), "Builder's report: "+copy+".") {
		t.Errorf("correction seed does not name the sealed report's copy %s:\n%s", copy, text)
	}
	copied, err := rt.Store.ReadFile(copy)
	if err != nil {
		t.Fatalf("read the copy %s: %v", copy, err)
	}
	if string(copied) != string(body) {
		t.Errorf("copy bytes = %q, want the sealed report's %q", copied, body)
	}
}

// TestChainReviewerSeedSaysNotAvailableForAnUnreadableGateLog pins the missing
// input: a check whose log was never written renders one path-free
// `not available:` clause, names no path for it, and still names the round's
// other inputs.
func TestChainReviewerSeedSaysNotAvailableForAnUnreadableGateLog(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	chainPlanDiffFixtures(fg)
	startedChain(t, rt, ChainOptions{})
	chainArmPassingGate(t, rt, "shop", "")
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	got := string(text)
	if !strings.Contains(got, "Check result: green; its output: not available:") {
		t.Errorf("reviewer seed does not word the missing gate log:\n%s", got)
	}
	if strings.Contains(got, rt.Store.GateLogPath("shop", 1)) {
		t.Errorf("reviewer seed names the gate log %s it cannot read:\n%s", rt.Store.GateLogPath("shop", 1), got)
	}
	diffCopy, ok := rt.Store.ChainInputPath("shop", rt.Store.DiffPath("shop", 1))
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", rt.Store.DiffPath("shop", 1))
	}
	if !strings.Contains(got, "This round's diff: "+diffCopy+".") {
		t.Errorf("reviewer seed does not name the round diff copy %s:\n%s", diffCopy, got)
	}
	if !strings.Contains(got, "Builder's report: "+rt.Store.ReportPath("shop", 1)+".") {
		t.Errorf("reviewer seed does not name the builder's report:\n%s", got)
	}
}

// TestChainCorrectionSeedNamesTheReviewersOutputFile pins the reader output: the
// correction seed names the reviewer's own artifact output path, not the flat
// NNN-report.md no reader ever writes.
func TestChainCorrectionSeedNamesTheReviewersOutputFile(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	reviewerRound := chainBinding(t, rt, "shop-rev").Round
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))

	planner := chainBinding(t, rt, "shop-plan")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-plan", planner.Round))
	if err != nil {
		t.Fatalf("read the correction prompt: %v", err)
	}
	got := string(text)

	rev := chainBinding(t, rt, "shop-rev")
	output := rt.Store.OutputPath("shop-rev", reviewerRound, bindingRole(rev), readerOutputLabel(rt, rev))
	if !strings.Contains(got, "Reviewer's output: "+output+".") {
		t.Errorf("correction seed does not name the reviewer's output %s:\n%s", output, got)
	}
	if flat := rt.Store.ReportPath("shop-rev", reviewerRound); strings.Contains(got, flat) {
		t.Errorf("correction seed names the flat report path %s:\n%s", flat, got)
	}
}

// TestRepairRoundSeedFramesThePlanAndListsEveryBuilderRound pins the repair
// seed: a red gate repairs round 1 and a red close after the spent budget seeds
// the reviewer, whose seed frames the plan as a whole, names the closing round
// a repair round sitting on round 1, and lists rounds 1 and 2 with openable
// prompt and report paths.
func TestRepairRoundSeedFramesThePlanAndListsEveryBuilderRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	b := chainBinding(t, rt, "shop")
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")

	// Round 1's red gate buys a repair: round 2 opens with a repair note.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	if b := chainBinding(t, rt, "shop"); b.Round != 2 {
		t.Fatalf("builder round = %d, want the repair round 2", b.Round)
	}

	// The repair round's own gate fails and the budget is spent, so the event
	// goes red to the reviewer rather than into a second repair.
	b = chainBinding(t, rt, "shop")
	b.RepairCount = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chainArmFailingGate(t, rt, "shop", "FAIL the same thing\n")
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	got := string(text)
	for _, want := range []string{
		"as a whole",
		"This closing round is " + workflow.BuilderRoundRepair + ", on top of round 1.",
		"Builder rounds of this plan:",
		"- round 1 prompt: " + rt.Store.PromptPath("shop", 1),
		"- round 1 report: " + rt.Store.ReportPath("shop", 1),
		"- round 2 prompt: " + rt.Store.PromptPath("shop", 2),
		"- round 2 report: " + rt.Store.ReportPath("shop", 2),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reviewer seed does not carry %q:\n%s", want, got)
		}
	}
	for _, path := range []string{
		rt.Store.PromptPath("shop", 1), rt.Store.ReportPath("shop", 1),
		rt.Store.PromptPath("shop", 2), rt.Store.ReportPath("shop", 2),
	} {
		if !rt.Store.DiskRegularFile(path) {
			t.Errorf("listed round path %s is not an openable file", path)
		}
	}
}

// TestChainReviewerSeedNamesTheCorrectionRoundKind pins the correction kind: a
// reviewer seed after a builder round that ran the planner's correction plan
// names that round a correction round on top of round 1, and lists both rounds.
func TestChainReviewerSeedNamesTheCorrectionRoundKind(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", "# Correction plan\n\nDo it.\n")
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	got := string(text)
	for _, want := range []string{
		"This closing round is " + workflow.BuilderRoundCorrection + ", on top of round 1.",
		"- round 1 prompt: " + rt.Store.PromptPath("shop", 1),
		"- round 2 prompt: " + rt.Store.PromptPath("shop", 2),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reviewer seed does not carry %q:\n%s", want, got)
		}
	}
}

// TestReviewerSeedNamesTheDiffFromWhenNoPlanDiffWasCaptured pins the
// diff-from-the-commit fallback: with no plan-start commit recorded on a plan 1
// chain, the reviewer seed names the chain base and tells the reviewer to diff
// the plan from it, and names no cumulative diff.
func TestReviewerSeedNamesTheDiffFromWhenNoPlanDiffWasCaptured(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.refSHA = map[string]string{"release/v1": "commit-base-456"}
	startedChain(t, rt, ChainOptions{Base: "release/v1"})

	// A chain started before the plan-start commit was recorded: no commit on
	// the row, so the seed must fall back to the base for plan 1.
	row := chainStoredRow(t, rt, "shop")
	if row.PlanStartCommit == "" || row.Base != "commit-base-456" {
		t.Fatalf("chain row = plan start %q base %q, want a recorded start and the cut base", row.PlanStartCommit, row.Base)
	}
	row.PlanStartCommit = ""
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.ChainPut(row) }); err != nil {
		t.Fatalf("clear the plan start: %v", err)
	}

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	text, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-rev", rev.Round))
	if err != nil {
		t.Fatalf("read the reviewer prompt: %v", err)
	}
	got := string(text)
	if want := "No cumulative plan diff was captured; diff the plan yourself from commit-base-456."; !strings.Contains(got, want) {
		t.Errorf("reviewer seed does not name the diff-from commit %q:\n%s", want, got)
	}
	if strings.Contains(got, "Plan diff, every round of this plan so far:") {
		t.Errorf("reviewer seed names a cumulative diff it does not have:\n%s", got)
	}
	if strings.Contains(got, "No cumulative plan diff was captured for this plan.") {
		t.Errorf("reviewer seed names no commit to diff from while a base is set:\n%s", got)
	}
}

// chainFence is the three-backtick fence a relevo block opens and closes with.
const chainFence = "```"

// chainRecapText is the block-free recap a reader writes after the message that
// carries its relevo block: it reads as the round's final text, so a reader
// that trusts only the output body would find no verdict or finding count.
const chainRecapText = "# Recap\n\nI summarise the round; the block was written earlier."

// chainStreamResultLine is one claude stream-json result line whose result is
// text, encoded so a test can plant a verdict-bearing message beside a recap.
func chainStreamResultLine(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": text,
	})
	if err != nil {
		t.Fatalf("marshal stream line: %v", err)
	}
	return string(b) + "\n"
}

// TestReviewerRecapAfterTheBlockKeepsTheBlockMessage pins the artifact rule: a
// reviewer that recaps after its block closes with a verdict from the stream,
// and its saved output is the block-carrying message, stripped of the block,
// never the recap.
func TestReviewerRecapAfterTheBlockKeepsTheBlockMessage(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	rev := chainBinding(t, rt, "shop-rev")
	stream := chainStreamResultLine(t, "I reviewed the round.\n\n"+chainFence+"relevo\nverdict: pass\n"+chainFence+"\n") +
		chainStreamResultLine(t, chainRecapText)
	if err := os.WriteFile(rt.Store.StreamPath("shop-rev", rev.Round), []byte(stream), 0o644); err != nil {
		t.Fatalf("write the reviewer stream: %v", err)
	}

	chainReaderClose(t, rt, "shop-rev", chainRecapText)

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusDone) {
		t.Fatalf("chain status = %q, want done: the verdict must come from the stream", row.Status)
	}
	outPath := rt.Store.OutputPath("shop-rev", rev.Round, bindingRole(rev), readerOutputLabel(rt, rev))
	out, err := rt.Store.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read the reviewer output: %v", err)
	}
	if strings.Contains(string(out), "verdict:") {
		t.Errorf("the reviewer's written output carries the verdict block:\n%s", out)
	}
	if !strings.Contains(string(out), "I reviewed the round") {
		t.Errorf("the reviewer's written output is not the block-carrying message:\n%s", out)
	}
	if strings.Contains(string(out), chainRecapText) {
		t.Errorf("the reviewer's written output is the recap:\n%s", out)
	}
}

// TestSecurityRecapAfterTheBlockStillYieldsTheFindings is the security twin:
// the scan's output file is a block-free recap and the finding count comes from
// the stream, so the fix planner is seeded rather than the chain halting.
func TestSecurityRecapAfterTheBlockStillYieldsTheFindings(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Security: ptr(true)})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	sec := chainBinding(t, rt, "shop-sec")
	stream := chainStreamResultLine(t, "I scanned the branch.\n\n"+chainFence+"relevo\nfindings: 2\n"+chainFence+"\n") +
		chainStreamResultLine(t, chainRecapText)
	if err := os.WriteFile(rt.Store.StreamPath("shop-sec", sec.Round), []byte(stream), 0o644); err != nil {
		t.Fatalf("write the security stream: %v", err)
	}

	chainReaderClose(t, rt, "shop-sec", chainRecapText)

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepPlanningFixes) {
		t.Fatalf("chain step = %q, want planning-fixes: the count must come from the stream", row.Step)
	}
	planner := chainBinding(t, rt, "shop-plan")
	if !HasPromptEntry(chainLog(t, rt, "shop-plan"), planner.Round) {
		t.Error("the planner must be seeded with a fix plan")
	}
	outPath := rt.Store.OutputPath("shop-sec", sec.Round, bindingRole(sec), readerOutputLabel(rt, sec))
	out, err := rt.Store.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read the security output: %v", err)
	}
	if strings.Contains(string(out), "findings:") {
		t.Errorf("the security member's written output carries the findings block:\n%s", out)
	}
}

// TestChainInputsAreRemovedWhenTheChainEnds pins item 5: a chain's inputs
// directory is swept when the chain ends done or stopped -- through the state
// machine, a stop, and the done verb -- while a halted chain keeps it, and the
// chain's plan-i.md copies are never touched.
func TestChainInputsAreRemovedWhenTheChainEnds(t *testing.T) {
	t.Parallel()

	openInputs := func(t *testing.T, rt Runtime) string {
		t.Helper()
		dir := rt.Store.ChainInputDir("shop")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir inputs: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "copy.patch"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write the inputs copy: %v", err)
		}
		return dir
	}
	assertPlanCopySurvives := func(t *testing.T, rt Runtime) {
		t.Helper()
		if _, err := os.Stat(rt.Store.ChainPlanPath("shop", 1)); err != nil {
			t.Errorf("the plan copy plan-1.md is gone: %v", err)
		}
	}

	t.Run("done", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		dir := openInputs(t, rt)
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
			t.Fatalf("chain status = %q, want done", row.Status)
		}
		assertInputsGone(t, dir)
		assertPlanCopySurvives(t, rt)
	})

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		dir := openInputs(t, rt)
		if _, err := Stop(context.Background(), rt, "shop", StopOptions{}); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusStopped) {
			t.Fatalf("chain status = %q, want stopped", row.Status)
		}
		assertInputsGone(t, dir)
		assertPlanCopySurvives(t, rt)
	})

	t.Run("halted keeps them", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		dir := openInputs(t, rt)
		chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
			t.Fatalf("chain status = %q, want halted", row.Status)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("a halted chain must keep its inputs: %v", err)
		}
	})

	t.Run("done verb", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		dir := openInputs(t, rt)
		if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
			t.Fatalf("ChainDone: %v", err)
		}
		assertInputsGone(t, dir)
		assertPlanCopySurvives(t, rt)
	})
}

// assertInputsGone asserts the chain's inputs directory was removed.
func assertInputsGone(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("inputs dir %s is still there (err %v), want it swept", dir, err)
	}
}
