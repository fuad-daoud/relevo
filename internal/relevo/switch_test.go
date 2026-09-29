package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
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
	if len(fr.specs) != 2 {
		t.Fatalf("starts = %d, want 2 (round 1 and its replacement)", len(fr.specs))
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

// TestExhaustionAfterResendStillSaysWhy pins #250 item 2: the second halt of
// a re-sent round still records its reason (Halt is always set, not only on
// the notifying branch), and a human's re-send is a fresh attempt -- it
// resets the round's switch budget and notification dedup, so the next
// exhaustion in the same round records its reason again.
//
// Mutation check: move the `b.Halt =` line in haltBinding back inside the
// `if b.HaltNotifiedRound != b.Round` guard and this test must fail, because
// the second halt below would leave Halt empty (HaltNotifiedRound is
// already back at b.Round from the first halt's dedup).
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
	// notified (dedup active, guard skipped) and confirm it still records
	// Halt without renotifying.
	if got.HaltNotifiedRound != got.Round {
		t.Fatalf("test setup: HaltNotifiedRound = %d, want %d (round already notified)", got.HaltNotifiedRound, got.Round)
	}
	got.Halt = ""
	beforeNotified := got.HaltNotifiedRound
	deduped, err := haltBinding(context.Background(), rt, got, "webshop: deduped halt check")
	if err != nil {
		t.Fatalf("haltBinding: %v", err)
	}
	if deduped.Halt == "" {
		t.Error("Halt is empty on a deduped halt, want the reason recorded")
	}
	if deduped.HaltNotifiedRound != beforeNotified {
		t.Errorf("HaltNotifiedRound = %d after a deduped halt, want unchanged %d", deduped.HaltNotifiedRound, beforeNotified)
	}
}

// TestRepeatedHaltKeepsHaltAt pins the fix in this round: HaltAt marks when
// a halt begins, not every tick that repeats it. haltBinding called three
// times with the same message keeps HaltAt at the first call's time; a
// different message restamps it; and clearing Halt by hand (as Send does)
// and repeating the same message restamps it too, since that is a fresh
// halt beginning from the caller's point of view.
//
// Mutation check: stamp HaltAt unconditionally (round 1's behavior) and the
// first assertion below fails, since the second and third calls would each
// advance it by a minute.
func TestRepeatedHaltKeepsHaltAt(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)

	first, err := haltBinding(context.Background(), rt, b, "webshop: same reason")
	if err != nil {
		t.Fatalf("haltBinding #1: %v", err)
	}
	firstHaltAt := first.HaltAt
	if firstHaltAt.IsZero() {
		t.Fatal("HaltAt is zero after the first halt, want set")
	}

	second, err := haltBinding(context.Background(), at(rt, time.Minute), first, "webshop: same reason")
	if err != nil {
		t.Fatalf("haltBinding #2: %v", err)
	}
	if !second.HaltAt.Equal(firstHaltAt) {
		t.Errorf("HaltAt = %v after a repeated halt, want unchanged %v", second.HaltAt, firstHaltAt)
	}
	if second.Halt != "same reason" {
		t.Errorf("Halt = %q after a repeated halt, want unchanged %q", second.Halt, "same reason")
	}

	third, err := haltBinding(context.Background(), at(rt, 2*time.Minute), second, "webshop: same reason")
	if err != nil {
		t.Fatalf("haltBinding #3: %v", err)
	}
	if !third.HaltAt.Equal(firstHaltAt) {
		t.Errorf("HaltAt = %v after a third repeated halt, want unchanged %v", third.HaltAt, firstHaltAt)
	}

	// A different message is a new halt: HaltAt restamps to that call's time.
	fourth, err := haltBinding(context.Background(), at(rt, 3*time.Minute), third, "webshop: different reason")
	if err != nil {
		t.Fatalf("haltBinding #4: %v", err)
	}
	wantFourthHaltAt := baseTime.Add(3 * time.Minute)
	if !fourth.HaltAt.Equal(wantFourthHaltAt) {
		t.Errorf("HaltAt = %v after a new-text halt, want %v", fourth.HaltAt, wantFourthHaltAt)
	}
	if fourth.Halt != "different reason" {
		t.Errorf("Halt = %q after a new-text halt, want %q", fourth.Halt, "different reason")
	}

	// Halt cleared by hand (as Send does) and the same message again is,
	// from haltBinding's point of view, a new halt beginning: HaltAt
	// restamps even though the text matches what it was before clearing.
	fourth.Halt = ""
	fifth, err := haltBinding(context.Background(), at(rt, 4*time.Minute), fourth, "webshop: different reason")
	if err != nil {
		t.Fatalf("haltBinding #5: %v", err)
	}
	wantFifthHaltAt := baseTime.Add(4 * time.Minute)
	if !fifth.HaltAt.Equal(wantFifthHaltAt) {
		t.Errorf("HaltAt = %v after a cleared-then-repeated halt, want %v", fifth.HaltAt, wantFifthHaltAt)
	}
	if fifth.Halt != "different reason" {
		t.Errorf("Halt = %q after a cleared-then-repeated halt, want %q", fifth.Halt, "different reason")
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
