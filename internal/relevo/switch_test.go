package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// runnerOf is the fakeRunner a fixture installed on rt.
func runnerOf(t *testing.T, rt Runtime) *fakeRunner {
	t.Helper()
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("runtime has no fakeRunner")
	}
	return fr
}

// at returns rt with its clock moved to baseTime + d.
func at(rt Runtime, d time.Duration) Runtime {
	rt.Now = func() time.Time { return baseTime.Add(d) }
	return rt
}

// switches returns the switch entries in webshop's log.
func switches(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindSwitch {
			out = append(out, e)
		}
	}
	return out
}

// TestSwitchRearmsSessionCursor pins switchBuilder's replacement-pane re-arm
// (#184, round 2 correction to base plan §4): the replacement process starts
// on the SAME round's plan, and the switch is recorded in the log.
//
// Mutation check (run and report): break switchBuilder's gated-candidate walk
// and this fails.
func TestGatedSwitchesAtOnce(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	fr := runnerOf(t, rt)
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %+v, want the old round's process killed", fr.kills)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1", got.Round)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0; a gated switch is uncounted", got.RoundSwitches)
	}

	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Fatalf("switch entries = %d, want 1", len(sw))
	}
	wantNote := "switched builder (rate-limited: 5h window): picked claude/test/m for builder: order #2; skipped agy/other/m (rate-limited until cleared)"
	if !strings.HasPrefix(sw[0].Note, wantNote) {
		t.Errorf("switch note = %q, want prefix %q", sw[0].Note, wantNote)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active after a gated switch", got.State)
	}
}

// TestGatedSwitchDoesNotCount checks the two halves of §4.5 together: a
// gated switch does not advance RoundSwitches, but the >= limit check at
// the top of switchBuilder still applies to it -- a binding already at the
// limit still halts instead of switching again.
func TestGatedSwitchDoesNotCount(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	limit := rt.Policy.SwitchLimit()
	b.RoundSwitches = limit - 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(runnerOf(t, rt).specs) != 2 {
		t.Fatalf("starts = %d, want 2 (round 1 and its replacement)", len(runnerOf(t, rt).specs))
	}
	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Fatalf("switch entries = %d, want 1", len(sw))
	}
	if got.RoundSwitches != limit-1 {
		t.Errorf("RoundSwitches = %d, want %d; a gated switch does not advance the count", got.RoundSwitches, limit-1)
	}

	rt2, b2 := sentSwitchable(t)
	b2.RoundSwitches = rt2.Policy.SwitchLimit()
	if err := rt2.Store.Save(b2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt2), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	got2, err := reconcile(t, rt2, b2)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got2.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you; the limit check still applies to a gated switch", got2.State)
	}
	if len(runnerOf(t, rt2).specs) != 1 {
		t.Errorf("starts = %d, want none beyond round 1", len(runnerOf(t, rt2).specs))
	}
}

func TestGatedIgnoresSpawnFailedGate(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	availability.RecordSpawnFailure(AvailabilityDeps(rt), "agy/other/m", "webshop", errors.New("x"))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := len(runnerOf(t, rt).specs); got != 1 {
		t.Errorf("starts = %d, want none beyond round 1 -- a running builder is not a failed spawn", got)
	}
	if len(runnerOf(t, rt).kills) != 0 {
		t.Errorf("kills = %+v, want none", runnerOf(t, rt).kills)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

func TestNoOrderHalts(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	rt.Policy = policy.Policy{}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if len(runnerOf(t, rt).specs) != 1 {
		t.Errorf("starts = %d, want none beyond round 1", len(runnerOf(t, rt).specs))
	}
	if !strings.Contains(got.Halt, "cannot switch") ||
		!strings.Contains(got.Halt, `candidates serve "builder"`) {
		t.Fatalf("Halt = %q, want it to contain 'cannot switch' and `candidates serve \"builder\"`", got.Halt)
	}
}

func TestMaxSwitchesZeroHalts(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	zero := 0
	rt.Policy.MaxSwitches = &zero
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if got := len(runnerOf(t, rt).specs); got != 1 {
		t.Errorf("starts = %d, want none beyond round 1", got)
	}
	if len(runnerOf(t, rt).kills) != 0 {
		t.Errorf("kills = %+v, want none", runnerOf(t, rt).kills)
	}
	if !strings.Contains(got.Halt, "already switched 0 time(s) this round (max_switches 0)") {
		t.Fatalf("Halt = %q, want it to contain the max_switches 0 message", got.Halt)
	}
}

// TestExhaustionAfterResendStillSaysWhy pins #250 item 2: a human's re-send is
// a fresh attempt -- it resets the round's switch budget and the notification
// dedup -- so the next exhaustion in the same round records its reason again.
//
// The reason is recorded on the notifying branch only, and a re-send is what
// makes the second exhaustion notify: Send clears the stamp along with Halt, so
// the second halt below enters the guard either way and Halt is set.
//
// Mutation check: write `b.Halt =` outside the `if b.HaltNotifiedRound !=
// b.Round` guard in haltBinding and the last assertion below must fail: a
// deduped halt queues no entry, so a reason written on that path is one no
// entry ever carried. See TestDedupedRoundCapHaltKeepsNotifiedArtifactCapReason
// for the cross-halt stamp that makes this cost something.
func TestExhaustionAfterResendStillSaysWhy(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 2)
	b.RoundSwitches = rt.Policy.SwitchLimit()
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "max_switches") {
		t.Fatalf("Halt = %q, want it to contain %q", got.Halt, "max_switches")
	}
	if got.HaltNotifiedRound != got.Round {
		t.Fatalf("HaltNotifiedRound = %d after the first halt, want %d", got.HaltNotifiedRound, got.Round)
	}

	// The human asks for another attempt: same round, a fresh Send.
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it again"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q after resend, want empty", got.Halt)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d after resend, want 0", got.HaltNotifiedRound)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d after resend, want 0", got.RoundSwitches)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v after resend, want nil", got.RoundExcluded)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s after resend, want active", got.State)
	}

	// Drive the same round to exhaustion again with the fresh budget.
	fr.script(got.Builder.PID, false)
	fr.exit(got.Builder.PID, 2)
	got.RoundSwitches = rt.Policy.SwitchLimit()
	if err := rt.Store.Save(got); err != nil {
		t.Fatal(err)
	}
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "max_switches") {
		t.Errorf("Halt = %q, want it to contain %q again", got.Halt, "max_switches")
	}

	// haltBinding's own invariant, isolated from Send's coupling (Send
	// happens to reset HaltNotifiedRound every time it clears Halt, which
	// means the two exhaustion halts above enter the notify guard either
	// way and cannot, by themselves, distinguish Halt being set inside vs.
	// outside it). Call haltBinding directly with the round already
	// notified (dedup active, guard skipped) and confirm it leaves the
	// notified reason standing rather than replacing it with a reason no
	// entry carries.
	if got.HaltNotifiedRound != got.Round {
		t.Fatalf("test setup: HaltNotifiedRound = %d, want %d (round already notified)", got.HaltNotifiedRound, got.Round)
	}
	notifiedHalt := got.Halt
	beforeNotified := got.HaltNotifiedRound
	deduped := haltOnce(t, rt, got, "webshop: deduped halt check")
	if deduped.Halt != notifiedHalt {
		t.Errorf("Halt = %q after a deduped halt, want the notified reason %q unchanged", deduped.Halt, notifiedHalt)
	}
	if deduped.HaltNotifiedRound != beforeNotified {
		t.Errorf("HaltNotifiedRound = %d after a deduped halt, want unchanged %d", deduped.HaltNotifiedRound, beforeNotified)
	}
}

// TestRepeatedHaltKeepsHaltAt pins HaltAt's rule: HaltAt marks when the
// notified halt began, not every tick that repeats it. haltBinding called
// three times with the same message keeps HaltAt at the first call's time; a
// deduped halt is a repeat by definition and so leaves Halt and HaltAt
// standing; and a fresh notification -- the stamp cleared as the round
// advance does -- restamps it even when the text is unchanged.
//
// Mutation check: stamp HaltAt unconditionally (writing it outside the
// HaltNotifiedRound guard) and the fourth call's assertion below fails,
// since it would advance by a minute.
func TestRepeatedHaltKeepsHaltAt(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)

	first := haltOnce(t, rt, b, "webshop: same reason")
	firstHaltAt := first.HaltAt
	if firstHaltAt.IsZero() {
		t.Fatal("HaltAt is zero after the first halt, want set")
	}

	second := haltOnce(t, at(rt, time.Minute), first, "webshop: same reason")
	if !second.HaltAt.Equal(firstHaltAt) {
		t.Errorf("HaltAt = %v after a repeated halt, want unchanged %v", second.HaltAt, firstHaltAt)
	}
	if second.Halt != "same reason" {
		t.Errorf("Halt = %q after a repeated halt, want unchanged %q", second.Halt, "same reason")
	}

	third := haltOnce(t, at(rt, 2*time.Minute), second, "webshop: same reason")
	if !third.HaltAt.Equal(firstHaltAt) {
		t.Errorf("HaltAt = %v after a third repeated halt, want unchanged %v", third.HaltAt, firstHaltAt)
	}

	// A different message is still a repeat, not a fresh notification: the
	// round is already notified, so this halt queues nothing and the reason
	// the mastermind was given stands.
	fourth := haltOnce(t, at(rt, 3*time.Minute), third, "webshop: different reason")
	if !fourth.HaltAt.Equal(firstHaltAt) {
		t.Errorf("HaltAt = %v after a deduped new-text halt, want unchanged %v", fourth.HaltAt, firstHaltAt)
	}
	if fourth.Halt != "same reason" {
		t.Errorf("Halt = %q after a deduped new-text halt, want the notified %q", fourth.Halt, "same reason")
	}

	// A fresh notification -- the stamp cleared as the round advance does --
	// is a new halt from haltBinding's point of view: HaltAt restamps even
	// though the text matches what it was before the advance.
	fourth.Halt = ""
	fourth.HaltNotifiedRound = 0
	fifth := haltOnce(t, at(rt, 4*time.Minute), fourth, "webshop: different reason")
	wantFifthHaltAt := baseTime.Add(4 * time.Minute)
	if !fifth.HaltAt.Equal(wantFifthHaltAt) {
		t.Errorf("HaltAt = %v after a fresh notification, want %v", fifth.HaltAt, wantFifthHaltAt)
	}
	if fifth.Halt != "different reason" {
		t.Errorf("Halt = %q after a fresh notification, want %q", fifth.Halt, "different reason")
	}
	if fifth.HaltNotifiedRound != fifth.Round {
		t.Errorf("HaltNotifiedRound = %d after a fresh notification, want %d", fifth.HaltNotifiedRound, fifth.Round)
	}
}

func TestAllGatedHalts(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testClaudeRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if got := len(runnerOf(t, rt).specs); got != 1 {
		t.Errorf("starts = %d, want none beyond round 1", got)
	}
	if len(runnerOf(t, rt).kills) != 0 {
		t.Errorf("kills = %+v, want none -- the halt precedes the close", runnerOf(t, rt).kills)
	}
	if !strings.Contains(got.Halt, `every candidate serving "builder" is gated`) {
		t.Fatalf("Halt = %q, want it to contain the ErrAllGated text", got.Halt)
	}
}

func TestSwitchRecordsOutgoingUsage(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	b.Builder.StreamStart = 4200
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	fu := &fakeUsage{
		peekSamples: []usage.Sample{
			{Provider: "other", Tokens: usage.Tokens{In: 500, Out: 200}, USD: 0.15, HasCost: true},
		},
	}
	rt.Usage = fu

	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "rate-limited"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = switchBuilder(context.Background(), rt, tx, b, "rate-limited", false, true)
		return err
	})
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if out.BuilderCandidate == b.BuilderCandidate {
		t.Fatal("switchBuilder did not switch candidate")
	}

	// Verify peek was called with StreamFrom == outgoing StreamStart
	if len(fu.peeks) == 0 {
		t.Fatal("peekUsage was not called")
	}
	if fu.peeks[0].StreamFrom != 4200 {
		t.Errorf("peek StreamFrom = %d, want 4200", fu.peeks[0].StreamFrom)
	}

	// Verify appended KindSwitch entry's Usage has those tokens
	sws := switches(t, rt)
	if len(sws) == 0 {
		t.Fatal("no KindSwitch entries found")
	}
	lastSw := sws[len(sws)-1]
	if lastSw.Usage == nil {
		t.Fatal("switch entry has nil Usage")
	}
	if lastSw.Usage.Tokens.In != 500 || lastSw.Usage.Tokens.Out != 200 {
		t.Errorf("switch entry tokens = %+v, want in:500 out:200", lastSw.Usage.Tokens)
	}
}

// TestSwitchKeepsTheStreamCursor is §7.2 N8: a mid-round switch to a builder
// of another kind keeps the round's stream cursor and its segment list, and
// the drain that follows renders only the bytes past the old offset -- the
// old harness's lines are not rendered a second time (or with the new kind).
func TestSwitchKeepsTheStreamCursor(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t) // round 1 open on agy/other/m
	seedLegacyLog(t, rt, "webshop", 1)

	// Round 1 has produced and rendered one agy line.
	streamWrite(t, rt, agyToolActive)
	before, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if before.Builder.StreamRound != 1 || before.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Fatalf("setup cursor = round %d offset %d, want 1/%d", before.Builder.StreamRound, before.Builder.StreamOffset, len(agyToolActive))
	}
	if len(before.Builder.StreamSegments) != 1 || before.Builder.StreamSegments[0].Kind != "agy" {
		t.Fatalf("setup segments = %+v, want one agy segment", before.Builder.StreamSegments)
	}
	oldOffset := before.Builder.StreamOffset

	// The switch replaces the endpoint mid-round with a different kind. Gate
	// the provider first, exactly as the rate-limit path does, so
	// resolveBuilder has to walk to a different candidate.
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "rate-limited"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	var switched store.Binding
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		switched, err = switchBuilder(context.Background(), rt, tx, before, "rate-limited", false, true)
		return err
	})
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if switched.Builder.Kind == "agy" {
		t.Fatalf("switch kept kind %q; the test needs a different kind", switched.Builder.Kind)
	}
	if switched.Builder.StreamRound != 1 || switched.Builder.StreamOffset != oldOffset {
		t.Errorf("cursor after the switch = round %d offset %d, want 1/%d", switched.Builder.StreamRound, switched.Builder.StreamOffset, oldOffset)
	}
	if len(switched.Builder.StreamSegments) != 2 {
		t.Fatalf("segments after the switch = %+v, want two", switched.Builder.StreamSegments)
	}
	if last := switched.Builder.StreamSegments[1]; last.Start != int64(len(agyToolActive)) || last.Kind != switched.Builder.Kind {
		t.Errorf("new segment = %+v, want {%d %s}", last, len(agyToolActive), switched.Builder.Kind)
	}

	// The replacement's own line arrives; a drain renders only bytes past
	// the old offset, so the old agy line is not rendered twice.
	streamWrite(t, rt, `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`+"\n")
	drainStream(rt, switched)
	log := readLog(t, rt)
	if n := strings.Count(log, "● run_command go test ./..."); n != 1 {
		t.Errorf("the old agy line appears %d time(s) in the log, want once:\n%s", n, log)
	}
	if !strings.Contains(log, "hi") {
		t.Errorf("log = %q, want the replacement's rendered line", log)
	}
}

// busyScopeRuntime is the #1058 setup in one call: a headless round in flight,
// scopes turned on, and this round's unit still reported loaded -- the state a
// provider outage leaves behind, since the old process is gone and systemd is
// only awaiting the reaper. It returns the runtime, the binding and the unit
// base name.
func busyScopeRuntime(t *testing.T) (Runtime, store.Binding, string) {
	t.Helper()
	fr := newFakeRunner()
	// Two providers, so the outage has somewhere to switch to.
	rt, b := gateOnLimitSetup(t, fr)
	rt.Scope = &spawn.ScopeSpec{}
	unit := scopeUnitName(b)
	fr.scopeActive = map[string]bool{unit: true}
	// systemd-run refuses a Start against a loaded unit, and a unit asked to
	// stop takes a moment to unload. Both are what makes #1058 an outage and
	// not a flag day: without them the fake would let any switch through.
	fr.refuseBusyScope = true
	fr.lingerProbes = 3
	return rt, b, unit
}

// TestSwitchResendEndsTheOldRoundsScope pins step 1's working half: a resend
// whose round scope is still loaded ends that unit before the replacement
// starts, so the replacement's Start is never refused by systemd -- and
// because the free succeeded, the candidate that did start records no
// spawn_failed gate for a failure that never happened.
func TestSwitchResendEndsTheOldRoundsScope(t *testing.T) {
	t.Parallel()

	rt, b, unit := busyScopeRuntime(t)
	b = outageExit(t, rt, b, jsonlLine(t, "agy-errors/results.jsonl", 6), 1)
	assertTailIsNotALimit(t, rt, b)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	fr := runnerOf(t, rt)
	if len(fr.scopeStops) != 1 || fr.scopeStops[0] != unit {
		t.Fatalf("scopeStops = %v, want exactly one entry, %s: the old round's unit was never freed", fr.scopeStops, unit)
	}
	// The replacement's Start must find the unit free. The fake runner does not
	// check scopes itself, so the ordering is pinned directly: the stop
	// happened before the Start that the switch performed.
	if fr.scopeStopsAt < 0 || fr.scopeStopsAt >= fr.started {
		t.Errorf("stop order: scope ended at %d, starts = %d; want the end before the replacement's Start", fr.scopeStopsAt, fr.started)
	}
	if fr.scopeActive[unit] {
		t.Errorf("scope %s is still active after the switch, want it freed", unit)
	}
	if got.State != store.StateActive {
		t.Fatalf("state = %s, want active: the replacement started", got.State)
	}
	if got.Builder.PID == 0 {
		t.Fatal("pid = 0, want the replacement's pid: nothing started")
	}
	for _, g := range availability.LedgerGates(AvailabilityDeps(rt), []string{got.BuilderCandidate}) {
		if g.Token == got.BuilderCandidate && g.Kind == availability.SpawnFailed {
			t.Errorf("gate = %+v, want no spawn_failed for a candidate that started", g)
		}
	}
}

// TestSwitchDefersWhileTheOldScopeIsBusy pins step 1's refusing half: a runner
// that can see the scope but not end it gets the whole switch deferred, with
// nothing written. The binding must come back byte-identical -- no gate, no
// halt, no switch entry, no budget spent -- so the next tick can retry.
func TestSwitchDefersWhileTheOldScopeIsBusy(t *testing.T) {
	t.Parallel()

	rt, b, unit := busyScopeRuntime(t)
	// The scope is loaded and the runner cannot end it: StopScope refuses, so
	// the unit stays active for the whole probe.
	runnerOf(t, rt).scopeStopErr = errors.New("unit is busy")
	b = outageExit(t, rt, b, jsonlLine(t, "agy-errors/results.jsonl", 6), 1)
	before := b

	got, err := reconcile(t, rt, b)
	if !errors.Is(err, ErrScopeActive) {
		t.Fatalf("Reconcile = %v, want errors.Is(..., ErrScopeActive)", err)
	}
	if got.BuilderCandidate != before.BuilderCandidate {
		t.Errorf("candidate = %q, want it unchanged at %q", got.BuilderCandidate, before.BuilderCandidate)
	}
	if got.RoundSwitches != before.RoundSwitches {
		t.Errorf("RoundSwitches = %d, want %d: a deferred switch spends nothing", got.RoundSwitches, before.RoundSwitches)
	}
	if len(got.RoundExcluded) != len(before.RoundExcluded) {
		t.Errorf("RoundExcluded = %v, want %v", got.RoundExcluded, before.RoundExcluded)
	}
	if unit != scopeUnitName(before) {
		t.Fatalf("unit = %q, want the round's own unit %q", unit, scopeUnitName(before))
	}
	if got.State != before.State || got.Halt != before.Halt {
		t.Errorf("state/halt = %s/%q, want %s/%q untouched", got.State, got.Halt, before.State, before.Halt)
	}
	if len(switches(t, rt)) != 0 {
		t.Errorf("switch entries = %d, want 0: nothing switched", len(switches(t, rt)))
	}
	if halts := haltEntries(t, rt, "webshop"); len(halts) != 0 {
		t.Errorf("halt entries = %d, want 0: a busy host is not a halt", len(halts))
	}
}

// scopeCollisionErr is systemd-run's own refusal text (#1058), verbatim in
// shape: the transient scope could not be started because the unit was already
// loaded -- the old round's scope, still reaping.
var scopeCollisionErr = errors.New("Failed to start transient scope unit: Unit relevo-round-local-webshop-1.scope was already loaded: hostfailure")

// TestScopeCollisionStartDoesNotGateTheCandidate pins steps 2 and 3 together:
// a Start the host refused for a scope collision records no spawn_failed gate
// for the replacement, does not exclude it from the round, does not spend the
// switch budget, and halts with text naming both the token and the host cause
// -- in wording that says it never started, so a human or `chain --resume`
// retries the right candidate.
//
// Mutation check: delete the isScopeBusy arm in startProcess and this fails on
// the gate alone; delete the hostStartError arm in switchBuilder and it fails
// on the halt wording alone.
func TestScopeCollisionStartDoesNotGateTheCandidate(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t)
	// Gate the outgoing provider so the resolution picks a different token.
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	// The replacement's own Start is the one the host refuses.
	fr := runnerOf(t, rt)
	fr.startErr = scopeCollisionErr

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
	if !strings.Contains(got.Halt, "already loaded") {
		t.Errorf("Halt = %q, want it to carry the host cause the runner reported", got.Halt)
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
			t.Errorf("gate = %+v, want no spawn_failed for a host-side collision", g)
		}
	}
}

// staleHaltedBinding stages the state every daemon revive in T4 walks into:
// the round timed out, so Halt is set, its notification key names THIS round,
// and the binding sits in needs_you. It is persisted, so the next tick reads
// it exactly as a daemon restart or a later decision point would.
func staleHaltedBinding(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()
	next := haltOnce(t, rt, b, "round timed out")
	next.State = store.StateNeedsYou
	if next.Halt == "" || next.HaltNotifiedRound != b.Round {
		t.Fatalf("staging: Halt = %q HaltNotifiedRound = %d, want a notified halt for round %d", next.Halt, next.HaltNotifiedRound, b.Round)
	}
	if err := rt.Store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return next
}

// TestRevivePathsClearAStaleHalt pins step 4 across all three daemon revives:
// a binding revived with State Active must carry no halt, or the per-round
// notification key left behind dedups the round's NEXT halt and it is never
// queued at all. Each subcase therefore stages a notified halt in this round,
// revives the binding, halts it again in the same round, and requires exactly
// one NEW halt entry with the new reason.
//
// Mutation check: delete any of the three clearHaltFields calls and that
// subcase sees zero new entries and the stale Halt.
func TestRevivePathsClearAStaleHalt(t *testing.T) {
	t.Parallel()

	t.Run("a successful switch", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, fr)
		b = staleHaltedBinding(t, rt, b)
		haltsBefore := len(haltEntries(t, rt, "webshop"))

		// The switch must run: gate the current provider so a different
		// candidate is picked.
		if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
			t.Fatalf("Unavailable: %v", err)
		}
		var got store.Binding
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			var err error
			got, err = switchBuilder(context.Background(), rt, tx, b, "rate-limited: 5h window", true, true)
			return err
		}); err != nil {
			t.Fatalf("switchBuilder: %v", err)
		}
		if got.Halt != "" {
			t.Fatalf("Halt = %q after a switch revived the round, want empty", got.Halt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d after the switch, want 0", got.HaltNotifiedRound)
		}

		// The round halts again in the same round: the new reason must be told.
		next := haltOnce(t, rt, got, "switched candidate timed out too")
		if next.Halt != "switched candidate timed out too" {
			t.Errorf("Halt = %q, want the new reason", next.Halt)
		}
		if got := haltEntries(t, rt, "webshop"); len(got) != haltsBefore+1 {
			t.Errorf("halt entries = %d, want %d: the new halt was deduped against the stale key", len(got), haltsBefore+1)
		}
	})

	t.Run("a nudge's resume", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := seedClaudeHeadless(t, fr)
		rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
		const sess = "S1"
		b.Builder.StreamSessionID = sess
		b = staleHaltedBinding(t, rt, b)
		haltsBefore := len(haltEntries(t, rt, "webshop"))

		fr.script(b.Builder.PID, false)
		fr.exit(b.Builder.PID, 0)
		if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rt = at(rt, time.Minute)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.State != store.StateActive || got.Builder.PID == 0 {
			t.Fatalf("state = %s pid = %d, want an active resumed round", got.State, got.Builder.PID)
		}
		if got.Halt != "" {
			t.Fatalf("Halt = %q after the resume revived the round, want empty", got.Halt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d after the resume, want 0", got.HaltNotifiedRound)
		}

		next := haltOnce(t, rt, got, "resumed candidate timed out too")
		if next.Halt != "resumed candidate timed out too" {
			t.Errorf("Halt = %q, want the new reason", next.Halt)
		}
		if got := haltEntries(t, rt, "webshop"); len(got) != haltsBefore+1 {
			t.Errorf("halt entries = %d, want %d: the new halt was deduped against the stale key", len(got), haltsBefore+1)
		}
	})

	t.Run("a relaunch after a daemon restart", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentHeadless(t, fr)
		b = staleHaltedBinding(t, rt, b)
		haltsBefore := len(haltEntries(t, rt, "webshop"))

		rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)
		rt.Watched = NewWatched()
		fr.script(b.Builder.PID, false)
		rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.State != store.StateActive || got.Builder.PID == 0 {
			t.Fatalf("state = %s pid = %d, want an active relaunched round", got.State, got.Builder.PID)
		}
		if got.Halt != "" {
			t.Fatalf("Halt = %q after the relaunch revived the round, want empty", got.Halt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d after the relaunch, want 0", got.HaltNotifiedRound)
		}

		next := haltOnce(t, rt, got, "relaunched candidate timed out too")
		if next.Halt != "relaunched candidate timed out too" {
			t.Errorf("Halt = %q, want the new reason", next.Halt)
		}
		if got := haltEntries(t, rt, "webshop"); len(got) != haltsBefore+1 {
			t.Errorf("halt entries = %d, want %d: the new halt was deduped against the stale key", len(got), haltsBefore+1)
		}
	})
}
