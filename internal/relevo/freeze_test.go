package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
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
