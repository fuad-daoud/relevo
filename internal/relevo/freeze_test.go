package relevo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// freezeFixtureLine is systemd-run's own stderr when its slice is frozen: the
// manager refused the transient scope, so the supervisor never ran and the
// stream carries no trailer. It is plain text, not harness JSON, which is why
// both the limit scan and the rendered tail still read it.
const freezeFixtureLine = `Failed to start transient scope unit: Cannot start frozen unit relevo.slice`

// freezeExit stages a frozen-start exit: the round's stream carries the
// manager's refusal as this process's own bytes, and the process is scripted
// dead with no trailer, so the code reads unknown.
func freezeExit(t *testing.T, rt Runtime, b store.Binding, raw string) store.Binding {
	t.Helper()
	streamWrite(t, rt, raw)
	b.Builder.StreamRound = b.Round
	b.Builder.StreamStart = 0
	fr := runnerOf(t, rt)
	fr.script(b.Builder.PID, false)
	return b
}

// freezeHolds returns the freeze-hold entries in webshop's log.
func freezeHolds(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindQueue && strings.HasPrefix(e.Note, freezeNotePrefix) {
			out = append(out, e)
		}
	}
	return out
}

// TestFreezeExitHoldsSameCandidate pins the hold: a frozen slice behind a
// no-report exit relaunches the same candidate uncounted, with no exclusion
// and no ledger write -- the host refused, not the candidate and not the
// provider. Mutation: delete the freeze arm and this fails on the exclusion
// alone, the same shape as the outage pin.
func TestFreezeExitHoldsSameCandidate(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = freezeExit(t, rt, b, freezeFixtureLine+"\n")
	if text := limitText(context.Background(), rt, b); text == "" {
		t.Fatal("the staged tail scans as empty text: this test would pass with the scan removed")
	}
	patterns := availability.LimitPatterns(AvailabilityDeps(rt), b.BuilderCandidate)
	if m, ok := availability.MatchLimit(limitText(context.Background(), rt, b), patterns, rt.Now(), rt.Policy.LimitGateDefault()); ok {
		t.Fatalf("the staged tail is a rate-limit line (%q): gateOnLimit would claim this exit before any freeze hold ran", m.Line)
	}

	wantCandidate := b.BuilderCandidate
	specsBefore := len(fr.specs)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.BuilderCandidate != wantCandidate {
		t.Errorf("BuilderCandidate = %q, want %q: a frozen slice keeps the same candidate", got.BuilderCandidate, wantCandidate)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0: a hold is not a switch", got.RoundSwitches)
	}
	if len(got.RoundExcluded) != 0 {
		t.Errorf("RoundExcluded = %v, want empty: a hold excludes nothing", got.RoundExcluded)
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
		t.Errorf("rate_limited entries = %+v, want none: a freeze takes no gate", rl)
	}
	for _, g := range availability.LedgerGates(AvailabilityDeps(rt), []string{wantCandidate}) {
		if g.Token == wantCandidate && g.Kind == availability.SpawnFailed {
			t.Errorf("gate = %+v, want no spawn_failed for a frozen slice", g)
		}
	}
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %+v, want none: a hold is not a switch", sw)
	}
	if holds := freezeHolds(t, rt); len(holds) != 1 {
		t.Fatalf("freeze holds = %+v, want exactly one", holds)
	} else if !strings.Contains(holds[0].Note, wantCandidate) {
		t.Errorf("hold note = %q, want it to name the relaunched candidate %s", holds[0].Note, wantCandidate)
	}
	if len(fr.specs) != specsBefore+1 {
		t.Errorf("specs = %d, want %d: the same candidate relaunches on the same round", len(fr.specs), specsBefore+1)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1: the replacement starts on the same round", got.Round)
	}
}

// TestFreezeExitPastBoundHaltsNamingSlice pins the bound: a slice still
// frozen after freezeHoldMax holds halts, naming the slice and the thaw, with
// no exclusion and no ledger write -- so a thawed `chain --resume` retries
// the right candidate. Mutation: delete the bound and this relaunches.
func TestFreezeExitPastBoundHaltsNamingSlice(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for i := 0; i < freezeHoldMax; i++ {
			if err := tx.AppendLog(b.Name, store.LogEntry{
				TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true,
				Note: freezeNotePrefix + " (seeded)",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed holds: %v", err)
	}
	b = freezeExit(t, rt, b, freezeFixtureLine+"\n")
	specsBefore := len(fr.specs)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you: a slice frozen past the bound halts", got.State)
	}
	if !strings.Contains(got.Halt, "relevo.slice") {
		t.Errorf("Halt = %q, want it to name the frozen slice", got.Halt)
	}
	if !strings.Contains(got.Halt, "thaw") {
		t.Errorf("Halt = %q, want it to name the thaw remedy", got.Halt)
	}
	if !strings.Contains(got.Halt, "chain --resume") {
		t.Errorf("Halt = %q, want it to name `chain --resume`", got.Halt)
	}
	if len(got.RoundExcluded) != 0 {
		t.Errorf("RoundExcluded = %v, want empty: a freeze halt excludes nothing", got.RoundExcluded)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0: a freeze halt spends no budget", got.RoundSwitches)
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
		t.Errorf("rate_limited entries = %+v, want none", rl)
	}
	if len(fr.specs) != specsBefore {
		t.Errorf("specs = %d, want %d: a halt starts nothing", len(fr.specs), specsBefore)
	}
}

// errFrozenStart is systemd-run's synchronous refusal text with the manager's
// frozen-unit cause inside, the shape classifySpawnStartError must read.
var errFrozenStart = freezeStartError()

func freezeStartError() error {
	return errors.New("failed to start transient scope unit: Cannot start frozen unit relevo.slice: hostfailure")
}

// TestFreezeStartDoesNotGateTheCandidate pins the send-time refusal: a Start
// the host refuses for a frozen slice records no spawn_failed gate, is not a
// spawnFailure, and halts with text naming both the token and the host cause.
// Mutation: delete the freeze arm in startProcess and this fails on the gate;
// delete the hostStartError arm and it fails on the halt wording.
func TestFreezeStartDoesNotGateTheCandidate(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	fr := runnerOf(t, rt)
	fr.startErr = errFrozenStart

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = switchBuilder(context.Background(), rt, tx, b, "provider outage: UNAVAILABLE (code 503)", false, true)
		return err
	})
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	newTok := got.BuilderCandidate
	if newTok == "" || newTok == b.BuilderCandidate {
		t.Fatalf("replacement candidate = %q, want a different token than %q", newTok, b.BuilderCandidate)
	}
	if !strings.Contains(got.Halt, newTok) {
		t.Errorf("Halt = %q, want it to name the replacement %s", got.Halt, newTok)
	}
	if !strings.Contains(got.Halt, "never started") {
		t.Errorf("Halt = %q, want it to say the replacement never started", got.Halt)
	}
	if !strings.Contains(strings.ToLower(got.Halt), "frozen") {
		t.Errorf("Halt = %q, want it to carry the frozen cause the runner reported", got.Halt)
	}
	for _, tok := range got.RoundExcluded {
		if tok == newTok {
			t.Errorf("RoundExcluded contains the replacement %s: a host refusal excludes nothing", tok)
		}
	}
	if got.RoundSwitches != b.RoundSwitches {
		t.Errorf("RoundSwitches = %d, want %d: a host that refuses the start spends no budget", got.RoundSwitches, b.RoundSwitches)
	}
	for _, g := range availability.LedgerGates(AvailabilityDeps(rt), []string{newTok}) {
		if g.Token == newTok && g.Kind == availability.SpawnFailed {
			t.Errorf("gate = %+v, want no spawn_failed for a frozen slice", g)
		}
	}
}

// TestFreezeHoldStartRefusalHalts pins the hold's own synchronous refusal: a
// freeze tail whose relaunch Start fails frozen halts naming the freeze, with
// nothing recorded. It is the exit path's answer to the same host refusal the
// send-time test pins on the switch path.
func TestFreezeHoldStartRefusalHalts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	b = freezeExit(t, rt, b, freezeFixtureLine+"\n")
	fr.startErr = errFrozenStart
	specsBefore := len(fr.specs)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "relevo.slice") || !strings.Contains(strings.ToLower(got.Halt), "frozen") {
		t.Errorf("Halt = %q, want it to name the frozen slice", got.Halt)
	}
	if len(got.RoundExcluded) != 0 {
		t.Errorf("RoundExcluded = %v, want empty", got.RoundExcluded)
	}
	if len(fr.specs) != specsBefore {
		t.Errorf("specs = %d, want %d: a refused relaunch starts nothing", len(fr.specs), specsBefore)
	}
}

// TestFreezeClassificationIsNotAReportPresentPath pins that the freeze hold
// never fires when a report close applies: with the report on disk the round
// closes through the unmarked-exit path, untouched.
func TestFreezeClassificationIsNotAReportPresentPath(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = freezeExit(t, rt, b, freezeFixtureLine+"\n")
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %+v, want none: a report on disk closes the round, nothing is held or switched", sw)
	}
	if holds := freezeHolds(t, rt); len(holds) != 0 {
		t.Errorf("freeze holds = %+v, want none: the report path is untouched", holds)
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
		t.Errorf("rate_limited entries = %+v, want none", rl)
	}
}

// freezeCheckStart fails a check's Start the way a frozen slice does: the
// host refuses the scope, so nothing runs. It returns what chainStartCheck
// reports, before the fix types it.
func freezeCheckStart(t *testing.T, rt Runtime, c db.ChainRow) (int, error) {
	t.Helper()
	var run int
	var serr error
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		run, err = chainStartCheck(context.Background(), rt, tx, c, "check", "make check")
		serr = err
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	return run, serr
}

// TestChainCheckFrozenStartIsCouldNotStart pins the send-time refusal: a Start
// the host refuses for a frozen slice settles the tracking row as
// could-not-start and returns the refusal typed, so the flow halts on the
// environment wording instead of recording a red row.
func TestChainCheckFrozenStartIsCouldNotStart(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	fr.startErr = errFrozenStart

	run, serr := freezeCheckStart(t, rt, c)
	if run != 0 {
		t.Errorf("run = %d, want 0: a refused start opens no run", run)
	}
	var envErr *EnvironmentError
	if !errors.As(serr, &envErr) {
		t.Fatalf("err = %v (%T), want a typed *EnvironmentError", serr, serr)
	}
	for _, want := range []string{"relevo.slice", "frozen", "thaw", "chain --resume"} {
		if !strings.Contains(serr.Error(), want) {
			t.Errorf("err = %q, want it to name %q", serr.Error(), want)
		}
	}
	row := loadChainCheck(t, rt, 1)
	if row.Result != chainCheckCouldNotStart {
		t.Errorf("row.Result = %q, want could-not-start: the row must never read red", row.Result)
	}
	if !strings.HasPrefix(row.Note, "environment: ") {
		t.Errorf("row.Note = %q, want the environment wording the halt reads", row.Note)
	}
	if len(fr.specs) != 0 {
		t.Errorf("starts = %d, want none: nothing ran", len(fr.specs))
	}
	if _, lerr := os.Stat(row.Log); !errors.Is(lerr, fs.ErrNotExist) {
		t.Errorf("a log exists for a check that never started: %v", lerr)
	}
}

// TestChainCheckFrozenStartWithoutScopeNamesTheSliceGenerically pins the
// scopes-off wording: with no template slice the refusal still types and the
// halt names the generic slice instead of an empty unit.
func TestChainCheckFrozenStartWithoutScopeNamesTheSliceGenerically(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	fr.startErr = errFrozenStart

	_, serr := freezeCheckStart(t, rt, c)
	var envErr *EnvironmentError
	if !errors.As(serr, &envErr) {
		t.Fatalf("err = %v (%T), want a typed *EnvironmentError", serr, serr)
	}
	if !strings.Contains(serr.Error(), "the slice is frozen") {
		t.Errorf("err = %q, want the generic frozen-slice wording", serr.Error())
	}
	if strings.Contains(serr.Error(), "thaw the slice") {
		t.Errorf("err = %q names an empty unit in the thaw command", serr.Error())
	}
}

// freezeCheckAdvanceWithLog writes body to the run's log, scripts its process
// dead with no exit trailer, and advances it once, returning what the advance
// settled.
func freezeCheckAdvanceWithLog(t *testing.T, rt Runtime, c db.ChainRow, run int, body string) (string, string, error) {
	t.Helper()
	row := loadChainCheck(t, rt, run)
	if err := os.WriteFile(row.Log, []byte(body), 0o644); err != nil {
		t.Fatalf("write check log: %v", err)
	}
	fr := rt.Runner.(*fakeRunner)
	for _, h := range fr.handles {
		if h.PID == row.PID {
			fr.script(h.PID, false)
		}
	}
	var result, logKey string
	var aerr error
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		result, logKey, err = chainAdvanceCheck(context.Background(), rt, tx, c, run)
		aerr = err
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	return result, logKey, aerr
}

// TestChainCheckFrozenLogRetriesTheCheckFresh pins the hold: a run that died
// with no trailer and the freeze signature in its log settles as
// could-not-start and starts a fresh run, instead of reddening.
func TestChainCheckFrozenLogRetriesTheCheckFresh(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	run := chainStartCheckForTest(t, rt, c, "check", "make check")

	result, _, aerr := freezeCheckAdvanceWithLog(t, rt, c, run, freezeFixtureLine+"\n")
	if aerr != nil {
		t.Fatalf("chainAdvanceCheck: %v", aerr)
	}
	if result != "" {
		t.Errorf("result = %q, want empty while the fresh run is in flight", result)
	}
	first := loadChainCheck(t, rt, run)
	if first.Result != chainCheckCouldNotStart {
		t.Errorf("run %d Result = %q, want could-not-start: the frozen run must never read red", run, first.Result)
	}
	if !strings.Contains(first.Note, "frozen") {
		t.Errorf("run %d Note = %q, want it to carry the freeze cause", run, first.Note)
	}
	sealed, serr := rt.Store.ReadFile(first.Log)
	if serr != nil {
		t.Fatalf("ReadFile(%s): %v", first.Log, serr)
	}
	if !strings.Contains(string(sealed), "frozen unit") {
		t.Errorf("sealed log = %q, want the freeze evidence", sealed)
	}
	retry := loadChainCheck(t, rt, run+1)
	if retry.Result != "" {
		t.Errorf("run %d Result = %q, want it in flight", run+1, retry.Result)
	}
	if retry.Step != "check" || retry.Command != "make check" {
		t.Errorf("run %d = %+v, want the same check step retried fresh", run+1, retry)
	}
	if len(fr.handles) != 2 {
		t.Fatalf("starts = %d, want the frozen run plus its fresh retry", len(fr.handles))
	}
	if retry.PID != fr.handles[1].PID {
		t.Errorf("retry PID = %d, want the fresh spawn %d", retry.PID, fr.handles[1].PID)
	}
}

// seedFrozenCheckHolds records bound could-not-start runs for the step, the
// shape a step frozen that many times already has.
func seedFrozenCheckHolds(t *testing.T, rt Runtime, name string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for i := 1; i <= freezeCheckHoldMax; i++ {
			if err := tx.ChainCheckPut(name, db.ChainCheckRow{
				Run: i, Step: "check", Visit: i, Command: "true",
				Result: chainCheckCouldNotStart,
				Note:   "environment: check could not start -- slice relevo.slice is frozen (seeded)",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed holds: %v", err)
	}
}

// TestChainCheckFrozenLogHaltsPastTheBound pins the terminal hold: a step
// frozen past the bound settles could-not-start with no fresh run, reporting
// the typed error the tick halts on.
func TestChainCheckFrozenLogHaltsPastTheBound(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	seedFrozenCheckHolds(t, rt, "shop")
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	if run != freezeCheckHoldMax+1 {
		t.Fatalf("run = %d, want %d after the seeded holds", run, freezeCheckHoldMax+1)
	}

	result, _, aerr := freezeCheckAdvanceWithLog(t, rt, c, run, freezeFixtureLine+"\n")
	if aerr == nil {
		t.Fatalf("chainAdvanceCheck = %q, want the could-not-start error past the bound", result)
	}
	var envErr *EnvironmentError
	if !errors.As(aerr, &envErr) {
		t.Fatalf("err = %v (%T), want a typed *EnvironmentError", aerr, aerr)
	}
	last := loadChainCheck(t, rt, run)
	if last.Result != chainCheckCouldNotStart {
		t.Errorf("run %d Result = %q, want could-not-start", run, last.Result)
	}
	if _, err := rt.Store.ChainCheck("shop", run+1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run %d exists past the bound: %v", run+1, err)
	}
	if len(fr.handles) != 1 {
		t.Errorf("starts = %d, want only the frozen run: past the bound nothing retries", len(fr.handles))
	}
}

// TestChainCheckFrozenTickHaltsTheChain pins the tick half of the terminal
// hold: the tick that advances a past-bound frozen run halts the chain naming
// the freeze and the remedy, with no red close, no repair, and no previous
// red for a later retry to repeat.
func TestChainCheckFrozenTickHaltsTheChain(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	startFlowChain(t, rt, flowCheckWorkflow)
	seedFrozenCheckHolds(t, rt, "shop")
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	run, row := flowCheckRun(t, rt, "shop")
	if run != freezeCheckHoldMax+1 {
		t.Fatalf("run = %d, want %d after the seeded holds", run, freezeCheckHoldMax+1)
	}
	if err := os.WriteFile(row.Log, []byte(freezeFixtureLine+"\n"), 0o644); err != nil {
		t.Fatalf("write check log: %v", err)
	}
	fr := rt.Runner.(*fakeRunner)
	fr.script(row.PID, false)

	tickChainChecks(context.Background(), rt)

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the freeze halt", c.Status, c.Reason)
	}
	for _, want := range []string{"relevo.slice", "frozen", "thaw", "chain --resume"} {
		if !strings.Contains(c.Reason, want) {
			t.Errorf("halt reason %q does not name %q", c.Reason, want)
		}
	}
	if strings.Contains(c.Reason, "The last check") {
		t.Errorf("halt reason %q appends a red-check detail to a hold", c.Reason)
	}
	st, serr := chain.WorkflowState(c)
	if serr != nil {
		t.Fatalf("WorkflowState: %v", serr)
	}
	if _, ok := st.Results["check"]; ok {
		t.Errorf("engine results = %+v, want no red for a hold", st.Results)
	}
	if chainRepeatRedCheck(rt, c, st, "check", row.Log) {
		t.Errorf("a could-not-start run counts as a previous red")
	}
}

// TestChainCheckFrozenHaltResumesTheCheckFresh pins the retryable half of the
// terminal hold: a thawed resume runs the check fresh on a new run instead of
// treating the frozen run as a previous red.
func TestChainCheckFrozenHaltResumesTheCheckFresh(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	startFlowChain(t, rt, flowCheckWorkflow)
	seedFrozenCheckHolds(t, rt, "shop")
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	run, row := flowCheckRun(t, rt, "shop")
	if err := os.WriteFile(row.Log, []byte(freezeFixtureLine+"\n"), 0o644); err != nil {
		t.Fatalf("write check log: %v", err)
	}
	rt.Runner.(*fakeRunner).script(row.PID, false)
	tickChainChecks(context.Background(), rt)
	if c := flowChainRow(t, rt); c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the freeze halt before the resume", c.Status, c.Reason)
	}

	// Thawed: later runs start clean, so the fresh run must not read the
	// frozen one as a previous red.
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want the resumed chain running", c.Status, c.Reason)
	}
	next, nrow := flowCheckRun(t, rt, "shop")
	if next != run+1 {
		t.Errorf("resumed run = %d, want %d: a thawed resume runs the check fresh", next, run+1)
	}
	st, serr := chain.WorkflowState(c)
	if serr != nil {
		t.Fatalf("WorkflowState: %v", serr)
	}
	if _, ok := st.Results["check"]; ok {
		t.Errorf("engine results = %+v, want no red carried across the resume", st.Results)
	}
	if chainRepeatRedCheck(rt, c, st, "check", nrow.Log) {
		t.Errorf("the frozen run counts as a previous red for the fresh run")
	}
}

// TestChainCheckFrozenRetryRefusedHalts pins the hold's own synchronous
// refusal: when the fresh retry's Start fails frozen too, its tracking row
// settles as could-not-start and the advance reports the typed error, so the
// tick halts with nothing started.
func TestChainCheckFrozenRetryRefusedHalts(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	fr.startErr = errFrozenStart

	_, _, aerr := freezeCheckAdvanceWithLog(t, rt, c, run, freezeFixtureLine+"\n")
	var envErr *EnvironmentError
	if !errors.As(aerr, &envErr) {
		t.Fatalf("err = %v (%T), want a typed *EnvironmentError", aerr, aerr)
	}
	if got := loadChainCheck(t, rt, run); got.Result != chainCheckCouldNotStart {
		t.Errorf("run %d Result = %q, want could-not-start", run, got.Result)
	}
	if got := loadChainCheck(t, rt, run+1); got.Result != chainCheckCouldNotStart {
		t.Errorf("run %d Result = %q, want the refused retry settled too", run+1, got.Result)
	}
	if _, err := rt.Store.ChainCheck("shop", run+2); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run %d exists after a refused retry: %v", run+2, err)
	}
	if len(fr.handles) != 1 {
		t.Errorf("starts = %d, want only the frozen run: the refused retry starts nothing", len(fr.handles))
	}
}

// TestChainCheckRetryStartFailureIsNotAFreezeHalt pins that only a freeze
// types the hold: a retry whose Start fails another way settles its row as an
// ordinary error and reports a plain error, which the tick will not halt on.
func TestChainCheckRetryStartFailureIsNotAFreezeHalt(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	fr.startErr = errors.New("boom")

	_, _, aerr := freezeCheckAdvanceWithLog(t, rt, c, run, freezeFixtureLine+"\n")
	if aerr == nil {
		t.Fatal("chainAdvanceCheck = nil, want the retry's start failure")
	}
	var envErr *EnvironmentError
	if errors.As(aerr, &envErr) {
		t.Fatalf("err = %v, want a plain error: only a freeze halts", aerr)
	}
	if got := loadChainCheck(t, rt, run+1); got.Result != "error" {
		t.Errorf("run %d Result = %q, want the ordinary error settlement", run+1, got.Result)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		halted, herr := chainHaltOnCouldNotStart(context.Background(), rt, tx, c, run, aerr)
		if halted || herr != nil {
			t.Errorf("chainHaltOnCouldNotStart = (%v, %v), want no halt for a plain error", halted, herr)
		}
		// A hold the store cannot load reports its load failure instead of
		// halting on a row it never read.
		frozen := &EnvironmentError{Reason: frozenCheckReason(rt)}
		if halted, herr := chainHaltOnCouldNotStart(context.Background(), rt, tx, c, 999, frozen); halted || herr == nil {
			t.Errorf("chainHaltOnCouldNotStart = (%v, %v), want the load failure", halted, herr)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}

// TestGateStepFrozenStartKeepsTheErrorRecord pins the containment: a round
// gate whose Start fails frozen keeps the error record it has always written,
// so only chain checks read the hold.
func TestGateStepFrozenStartKeepsTheErrorRecord(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	fr := newFakeRunner()
	rt.Runner = fr
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	b.Gate = "make check"
	fr.startErr = errFrozenStart

	var done bool
	var rec *store.GateRecord
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		_, done, rec, err = gateStep(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("gateStep: %v", err)
	}
	if !done {
		t.Errorf("done = false, want the failed start to finish the gate")
	}
	if rec == nil {
		t.Fatalf("rec = nil, want the error record a gate start failure writes")
	}
	if rec.Result != "error" {
		t.Errorf("rec.Result = %q, want error", rec.Result)
	}
	if !strings.Contains(rec.Note, "frozen") {
		t.Errorf("rec.Note = %q, want it to carry the host cause", rec.Note)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.GateRun != nil {
		t.Errorf("GateRun = %+v, want none: nothing started", got.GateRun)
	}
}

// TestServedCheckFrozenStartRecordsTheErrorRun pins the served containment: a
// Start the host refuses for a frozen slice records the errored run the
// function has always written, worded as the gate's own refusal, instead of
// failing the create with no record of what was tried.
func TestServedCheckFrozenStartRecordsTheErrorRun(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.TmpRoot = t.TempDir()
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	fr.startErr = errFrozenStart

	run, err := startServedCheck(context.Background(), rt, b, remote.CreateCheckRequest{ID: "c1", Command: "make check"})
	if err != nil {
		t.Fatalf("startServedCheck: %v, want the errored run, not a refusal", err)
	}
	if run == nil {
		t.Fatal("run = nil, want the errored run recorded")
	}
	if run.Result != "error" {
		t.Errorf("run.Result = %q, want error", run.Result)
	}
	for _, want := range []string{"gate could not start", "relevo.slice", "frozen", "thaw"} {
		if !strings.Contains(run.Note, want) {
			t.Errorf("run.Note = %q, want it to name %q", run.Note, want)
		}
	}
	if strings.Contains(run.Note, "chain --resume") {
		t.Errorf("run.Note = %q, want no chain remedy for a served client", run.Note)
	}
	if run.PID != 0 {
		t.Errorf("run.PID = %d, want none: nothing started", run.PID)
	}
	if len(fr.specs) != 0 {
		t.Errorf("starts = %d, want none: nothing ran", len(fr.specs))
	}
}

// TestGateStepFrozenRetrySettlesTheErrorRecord pins the advance containment:
// a gate lost to a restart whose single re-run Start fails frozen settles
// done with the error record, storing no run and logging no restart.
func TestGateStepFrozenRetrySettlesTheErrorRecord(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.StartedAt = baseTime
	rt.Watched = NewWatched()
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	b.Gate = "make check"
	b.GateRun = &store.GateRun{
		PID:       9001,
		StartedAt: baseTime.Add(-time.Minute).Unix(),
		Round:     b.Round,
		Command:   "make check",
		Attempt:   0,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(9001, false) // exited; no exit() set: no trailer
	fr.startErr = errFrozenStart

	var got store.Binding
	var done bool
	var rec *store.GateRecord
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, done, rec, err = gateStep(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("gateStep: %v", err)
	}

	if !done {
		t.Error("done = false, want the refused retry to finish the gate")
	}
	if rec == nil {
		t.Fatalf("rec = nil, want the error record a refused retry settles")
	}
	if rec.Result != "error" {
		t.Errorf("rec.Result = %q, want error", rec.Result)
	}
	for _, want := range []string{"gate could not start", "relevo.slice", "frozen", "thaw"} {
		if !strings.Contains(rec.Note, want) {
			t.Errorf("rec.Note = %q, want it to name %q", rec.Note, want)
		}
	}
	if strings.Contains(rec.Note, "chain --resume") {
		t.Errorf("rec.Note = %q, want the gate wording, never the check's", rec.Note)
	}
	if got.GateRun != nil {
		t.Errorf("GateRun = %+v, want none: a refused retry stores no run", got.GateRun)
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindGate && strings.Contains(e.Note, "restarted") {
			t.Errorf("restart entry = %+v, want none: nothing restarted", e)
		}
	}
	if len(fr.specs) != 0 {
		t.Errorf("starts = %d, want none: a refused retry starts nothing", len(fr.specs))
	}
}

// TestChainCheckFrozenRetrySettlesTheLostRun pins the chain half of a frozen
// retry: a check lost to a restart whose re-run Start fails frozen settles
// the lost run as could-not-start, counting toward the bound, instead of
// dropping the loss.
func TestChainCheckFrozenRetrySettlesTheLostRun(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)

	// The daemon that started the run has gone: the recorded start predates
	// this one, and the watch set never saw the process.
	rt.StartedAt = time.Unix(row.StartedAt, 0).Add(time.Minute)
	rt.Watched = NewWatched()
	fr.startErr = errFrozenStart

	result, _, aerr := freezeCheckAdvanceWithLog(t, rt, c, run, freezeFixtureLine+"\n")
	var envErr *EnvironmentError
	if !errors.As(aerr, &envErr) {
		t.Fatalf("err = %v (%T), want a typed *EnvironmentError", aerr, aerr)
	}
	if result != "" {
		t.Errorf("result = %q, want empty: the refused retry holds, never red", result)
	}
	lost := loadChainCheck(t, rt, run)
	if lost.Result != chainCheckCouldNotStart {
		t.Errorf("run %d Result = %q, want the lost run settled could-not-start", run, lost.Result)
	}
	for _, want := range []string{"frozen", "chain --resume"} {
		if !strings.Contains(lost.Note, want) {
			t.Errorf("run %d Note = %q, want it to carry %q", run, lost.Note, want)
		}
	}
	retry := loadChainCheck(t, rt, run+1)
	if retry.Result != chainCheckCouldNotStart {
		t.Errorf("run %d Result = %q, want the refused retry settled too", run+1, retry.Result)
	}
	if _, err := rt.Store.ChainCheck("shop", run+2); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run %d exists after a refused retry: %v", run+2, err)
	}
	if len(fr.handles) != 1 {
		t.Errorf("starts = %d, want only the lost run: the refused retry starts nothing", len(fr.handles))
	}
}

// TestChainCheckFrozenLogWithTrailerStaysRed pins the no-trailer guard: a run
// that exited with a genuine trailer stays red even when its log carries the
// freeze signature, starting no fresh run.
func TestChainCheckFrozenLogWithTrailerStaysRed(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100}
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)
	fr.exit(row.PID, 2)

	result, _, aerr := freezeCheckAdvanceWithLog(t, rt, c, run, freezeFixtureLine+"\n")
	if aerr != nil {
		t.Fatalf("chainAdvanceCheck: %v", aerr)
	}
	if result != chainCheckRed {
		t.Errorf("result = %q, want red: a trailered exit is the command's own failure", result)
	}
	got := loadChainCheck(t, rt, run)
	if got.Result != "fail" || got.ExitCode != 2 {
		t.Errorf("row = %+v, want result fail and exit 2", got)
	}
	if _, err := rt.Store.ChainCheck("shop", run+1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run %d exists for a red run: %v", run+1, err)
	}
	if len(fr.handles) != 1 {
		t.Errorf("starts = %d, want only the red run: no hold retries it", len(fr.handles))
	}
}
