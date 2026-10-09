package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestSendDeferQueues pins #285's staging half: Send(Defer) writes the plan
// and opens the round exactly as a normal Send does, but never spawns --
// pid stays 0, RoundStartedAt stays zero, QueuedAt is stamped, and
// RoundStateOf reports queued.
//
// Mutation check: drop the `!deferred` guard around startRound in send.go
// and this fails on fr.specs no longer being empty.
func TestSendDeferQueues(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.PID != 0 {
		t.Errorf("PID = %d, want 0 (no spawn)", got.Builder.PID)
	}
	if !got.RoundStartedAt.IsZero() {
		t.Errorf("RoundStartedAt = %v, want zero", got.RoundStartedAt)
	}
	if !got.QueuedAt.Equal(baseTime) {
		t.Errorf("QueuedAt = %v, want %v", got.QueuedAt, baseTime)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var planCount int
	for _, e := range entries {
		if e.Kind == store.KindPrompt {
			planCount++
		}
	}
	if planCount != 1 {
		t.Errorf("plan entries = %d, want 1", planCount)
	}

	if state := RoundStateOf(got, entries); state != remote.RoundQueued {
		t.Errorf("RoundStateOf = %v, want %v", state, remote.RoundQueued)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0 (Runner.Start not called)", len(fr.specs))
	}
}

// TestAdmitStartsQueuedRound pins #285's admit half: Admit spawns the
// process, stamps RoundStartedAt at the admitting clock, zeroes QueuedAt,
// and logs how long the round waited.
//
// Mutation check: drop the `age` formatting (hardcode "0s") in queue.go and
// this fails on the note not containing "1m30s".

// TestAdmitStartsQueuedRound pins #285's admit half: Admit spawns the
// process, stamps RoundStartedAt at the admitting clock, zeroes QueuedAt,
// and logs how long the round waited.
//
// Mutation check: drop the `age` formatting (hardcode "0s") in queue.go and
// this fails on the note not containing "1m30s".
func TestAdmitStartsQueuedRound(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}

	admitRt := at(rt, 90*time.Second)
	if err := Admit(context.Background(), admitRt, "webshop"); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(fr.handles) != 1 || got.Builder.PID != fr.handles[0].PID {
		t.Errorf("PID = %d, want the runner's handle pid", got.Builder.PID)
	}
	wantStarted := baseTime.Add(90 * time.Second)
	if !got.RoundStartedAt.Equal(wantStarted) {
		t.Errorf("RoundStartedAt = %v, want %v", got.RoundStartedAt, wantStarted)
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero", got.QueuedAt)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindQueue || last.Note != "started after 1m30s queued" {
		t.Errorf("last entry = %+v, want KindQueue with note %q", last, "started after 1m30s queued")
	}
}

// TestAdmitNotQueued pins Admit's guard: a binding that was never deferred
// (QueuedAt zero) is refused, and nothing spawns.

// TestAdmitNotQueued pins Admit's guard: a binding that was never deferred
// (QueuedAt zero) is refused, and nothing spawns.
func TestAdmitNotQueued(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)

	err := Admit(context.Background(), rt, b.Name)
	if !errors.Is(err, ErrNotQueued) {
		t.Fatalf("Admit: err = %v, want ErrNotQueued", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0 (no spawn)", len(fr.specs))
	}
}

// TestAdmitSpawnFailure pins Admit's failure path: mirrors Send's own
// spawn-failure handling, and QueuedAt is zeroed so the round is never
// re-admitted.

// TestAdmitSpawnFailure pins Admit's failure path: mirrors Send's own
// spawn-failure handling, and QueuedAt is zeroed so the round is never
// re-admitted.
func TestAdmitSpawnFailure(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	fr.startErr = errors.New("boom: no such binary")

	err := Admit(context.Background(), rt, "webshop")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Admit: err = %v, want it to wrap the spawn error", err)
	}

	got, loadErr := rt.Store.Load("webshop")
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "builder spawn failed") {
		t.Errorf("Halt = %q, want it to contain %q", got.Halt, "builder spawn failed")
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero (never re-admitted)", got.QueuedAt)
	}
}

// TestAdmitGatedSwitches pins Admit's gated-candidate branch: when the
// queued round's candidate is gated by the time a slot frees up, Admit
// switches to the next candidate (uncounted, no pane/process to close) and
// its KindQueue note says so.
//
// Mutation check: drop the `gatedBuilder` branch in queue.go (always call
// startRound) and this fails on BuilderCandidate staying "agy/other/m".

// TestAdmitGatedSwitches pins Admit's gated-candidate branch: when the
// queued round's candidate is gated by the time a slot frees up, Admit
// switches to the next candidate (uncounted, no pane/process to close) and
// its KindQueue note says so.
//
// Mutation check: drop the `gatedBuilder` branch in queue.go (always call
// startRound) and this fails on BuilderCandidate staying "agy/other/m".
// TestAdmitSwitchFailureRecordsNoStart pins the guard half of this change:
// when the switch Admit performs cannot resolve a replacement, switchBuilder
// queues the binding's halt entry and returns -- with no process and no
// error. Admit must not stamp RoundStartedAt or log "started after ... queued"
// for a round that never began, and must not leave the binding switchable
// again.
//
// Mutation check: delete the `b.Builder.PID == 0` guard in queue.go and this
// fails on RoundStartedAt no longer being zero and the started note appearing.
func TestAdmitSwitchFailureRecordsNoStart(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	// Two providers, so gating the first leaves the second reachable, and the
	// read tier the binding carries is one opencode refuses.
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"first","model":"m","roles":["builder","reviewer"]},
	  {"harness":"opencode","provider":"second","model":"m","roles":["builder"]}]`)
	rt.Policy = orderOf("builder", "claude/first/m", "opencode/second/m")

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/first/m", MasterMindID: testMasterMindName, CWD: "/repo", Tier: "read",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/first/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	// The fault is the queued entry switchBuilder leaves behind, not a return
	// value: Admit runs in the daemon, so a plain error would be dropped.
	if err := Admit(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.RoundStartedAt.IsZero() {
		t.Errorf("RoundStartedAt = %v, want zero: nothing started", got.RoundStartedAt)
	}
	if got.Builder.PID != 0 {
		t.Errorf("pid = %d, want 0", got.Builder.PID)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want %s", got.State, store.StateBroken)
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero (the same failed switch must not be retried every tick)", got.QueuedAt)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Kind == store.KindQueue && strings.Contains(e.Note, "started after") {
			t.Errorf("entry = %+v, want no started note for a round that did not start", e)
		}
	}
	if halts := haltEntries(t, rt, "webshop"); len(halts) != 1 {
		t.Errorf("halt entries = %d, want 1: the switch failure owes its entry: %+v", len(halts), halts)
	}
}

// TestAdmitSwitchFailureQueuesOneHaltAfterAFailedQueue pins the switch
// failure's halt entry against a queue that cannot write it: the first Admit
// records the reason on the binding and owes the entry, and the next Admit --
// once the log has room again -- writes exactly one.
//
// switchBuilder set b.Halt before it queued the entry, so a queue that failed
// left a binding that read as already halted: Admit's spawn-failure branch kept
// that reason, zeroed QueuedAt, and wrote nothing, and nothing retried it. The
// entry the switch owed its MasterMind was never queued at all.
//
// Mutation check: restore the b.Halt assignment above queueBrokenHalt in
// switch.go and this fails on the second Admit refusing (QueuedAt zeroed) with
// no halt entry anywhere in the log.
func TestAdmitSwitchFailureQueuesOneHaltAfterAFailedQueue(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	// Two providers, so gating the first leaves the second reachable, and the
	// read tier the binding carries is one opencode refuses.
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"first","model":"m","roles":["builder","reviewer"]},
	  {"harness":"opencode","provider":"second","model":"m","roles":["builder"]}]`)
	rt.Policy = orderOf("builder", "claude/first/m", "opencode/second/m")

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/first/m", MasterMindID: testMasterMindName, CWD: "/repo", Tier: "read",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/first/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	// A log with no room left is the one queue fault a binding cannot write
	// past, and this test needs nothing else appended.
	fillLogLeavingRoom(t, rt, "webshop", 0)

	if err := Admit(context.Background(), rt, "webshop"); err == nil {
		t.Fatal("Admit: err = nil, want the queue failure the full log answers")
	}
	if halts := haltEntries(t, rt, "webshop"); len(halts) != 0 {
		t.Fatalf("halt entries = %d, want 0 while the log is full: %+v", len(halts), halts)
	}

	freeLogRoom(t, rt, "webshop")
	if err := Admit(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("second Admit: %v", err)
	}

	halts := haltEntries(t, rt, "webshop")
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1: the switch failure owes its MasterMind one: %+v", len(halts), halts)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero", got.QueuedAt)
	}
	if got.HaltNotifiedRound != got.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d: the dedup key must be stamped", got.HaltNotifiedRound, got.Round)
	}
}

// TestAdmitSpawnFailureQueuesAHaltEntry pins Admit's halt entry: it halts
// through the shared path, so a spawn failure owes its MasterMind the same one
// entry every other NEEDS YOU owes. It used to set State and Halt by hand and
// queue nothing -- the binding read as needs-you with no payload, and a
// MasterMind with no push route was never told at all.
//
// Mutation check: restore the hand-set State/Halt block in queue.go and this
// fails on haltEntries = 0 and on the empty wait payload.
func TestAdmitSpawnFailureQueuesAHaltEntry(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	fr.startErr = errors.New("boom: no such binary")

	if err := Admit(context.Background(), rt, "webshop"); err == nil {
		t.Fatal("Admit: err = nil, want the spawn error")
	}

	halts := haltEntries(t, rt, "webshop")
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1: %+v", len(halts), halts)
	}
	if !strings.Contains(halts[0].Payload, "builder spawn failed") || !strings.Contains(halts[0].Payload, "boom") {
		t.Errorf("entry.Payload = %q, want it to carry the spawn failure", halts[0].Payload)
	}
	if !strings.Contains(halts[0].Payload, "relevo status --name webshop") {
		t.Errorf("entry.Payload = %q, want it to carry the status pointer", halts[0].Payload)
	}

	// The entry is what wait has to work with, so the wait reports the reason
	// rather than an empty needs-you.
	name, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitNeedsYou || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want needs-you for webshop", name, res)
	}
	if res.Payload == "" {
		t.Error("Wait Payload is empty, want the queued spawn failure")
	}
	if !strings.Contains(res.Payload, "builder spawn failed") {
		t.Errorf("Wait Payload = %q, want the spawn failure", res.Payload)
	}
}

// TestAdmitSwitchFailureCarriesTheSwitchReason pins what a failed switch says
// when the entry it owes cannot be written. switchBuilder returns the binding
// with no Halt in that case -- the entry the reason would have carried was never
// queued -- so Admit's spawn-failure halt is what runs, and it names only the
// append that failed. Admit runs in the daemon, where that error is the fault's
// only report, so the switch the round actually broke on has to ride it.
//
// Mutation check: drop the reason from the error switchBuilder returns and this
// fails on the Admit error naming no switch.
func TestAdmitSwitchFailureCarriesTheSwitchReason(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"first","model":"m","roles":["builder","reviewer"]},
	  {"harness":"opencode","provider":"second","model":"m","roles":["builder"]}]`)
	rt.Policy = orderOf("builder", "claude/first/m", "opencode/second/m")

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/first/m", MasterMindID: testMasterMindName, CWD: "/repo", Tier: "read",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/first/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	// A log with no room left: the entry the broken switch owes cannot be
	// queued, which is the case that hands the reason to Admit's own halt.
	fillLogLeavingRoom(t, rt, "webshop", 0)

	err := Admit(context.Background(), rt, "webshop")
	if err == nil {
		t.Fatal("Admit: err = nil, want the switch failure and the halt it could not queue")
	}
	if !strings.Contains(err.Error(), "switching to") {
		t.Errorf("Admit error = %q, want it to carry the switch the round broke on", err)
	}
}

// TestAdmitSwitchFailureWithAFailingHaltAppendLeavesNoSilentHalt pins that a
// switch failure whose entry cannot be written never reads as notified.
//
// haltBindingKind stamps the per-round key before it queues the entry, so a
// failed append left a binding that already carried a reason, a stamp and no
// entry. Admit's spawn-failure branch read that reason as "the switch already
// halted this", saved Active with QueuedAt cleared, and returned -- and nothing
// retried it. The round sat active with nothing running and a halt nobody was
// told about, and the next tick returned at the no-process branch.
func TestAdmitSwitchFailureWithAFailingHaltAppendLeavesNoSilentHalt(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"first","model":"m","roles":["builder","reviewer"]},
	  {"harness":"opencode","provider":"second","model":"m","roles":["builder"]}]`)
	rt.Policy = orderOf("builder", "claude/first/m", "opencode/second/m")

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/first/m", MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/first/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	// One entry of room: the switch's own audit entry fits, the halt entry that
	// follows it does not.
	fillLogLeavingRoom(t, rt, "webshop", 1)
	// The replacement resolves and the spawn does not, which is the path that
	// halts through haltAndSettle and hands Admit a binding that reads as
	// already halted.
	fr.startErr = errors.New("boom: no such binary")

	if err := Admit(context.Background(), rt, "webshop"); err == nil {
		t.Fatal("Admit: err = nil, want the spawn failure after the switch")
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State == store.StateActive && got.Halt != "" {
		t.Errorf("binding = active with halt %q, want no halt on a binding no tick will retry", got.Halt)
	}
	if got.HaltNotifiedRound == got.Round && len(haltEntries(t, rt, "webshop")) == 0 {
		t.Errorf("binding = notified for round %d with no halt entry, want an unnotified halt a retry can queue",
			got.Round)
	}

	freeLogRoom(t, rt, "webshop")
	if err := Admit(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("second Admit: %v", err)
	}

	halts := haltEntries(t, rt, "webshop")
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1 once the log has room again: %+v", len(halts), halts)
	}
	got, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load after: %v", err)
	}
	if got.State == store.StateActive {
		t.Errorf("state = active with halt %q, want the binding to have settled the failure", got.Halt)
	}
}

func TestAdmitGatedSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	if err := Admit(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if len(fr.specs) != 1 || fr.specs[0].Argv[0] != "claude" {
		t.Fatalf("want a claude replacement: %+v", fr.specs)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (a gated switch is uncounted)", got.RoundSwitches)
	}
	if got.QueuedAt.IsZero() == false {
		t.Errorf("QueuedAt = %v, want zero", got.QueuedAt)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindQueue || !strings.Contains(last.Note, "(switched: gated while queued)") {
		t.Errorf("last entry = %+v, want a KindQueue note containing %q", last, "(switched: gated while queued)")
	}
}

// failingConfirmDeliverer admits a payload and then fails the read-back, which
// is the shape that leaves switchBuilder's queueBrokenHalt entry queued, the
// per-round key stamped, and the binding settled Broken with the delivery
// error returned -- the state #1051's Admit check has to recognise.
type failingConfirmDeliverer struct {
	deliverCalls int
}

func (d *failingConfirmDeliverer) Deliver(_ context.Context, _ store.Endpoint, _, _ string, _ time.Time) (delivery.Outcome, string, error) {
	d.deliverCalls++
	return delivery.OutcomeAdmitted, "posted; awaiting the session", nil
}

func (d *failingConfirmDeliverer) Confirm(_ context.Context, _ store.Endpoint, _ string, _ time.Time) (delivery.Outcome, string, error) {
	return delivery.OutcomeNotMine, "", errors.New("session read-back failed")
}

func (d *failingConfirmDeliverer) ConfirmOnce(_ context.Context, _ store.Endpoint, _ string, _ time.Time) (delivery.Outcome, string, error) {
	return delivery.OutcomeNotMine, "", errors.New("session read-back failed")
}

func (d *failingConfirmDeliverer) AdmitHorizon() time.Duration { return 0 }

// TestAdmitKeepsAnAlreadyNotifiedBrokenHalt pins step 5's broken half: a
// binding that comes back from the switch already Broken with this round's
// notification key stamped must be recognised as already halted. Falling
// through to haltAndSettle dedupes the entry, fails the delivery again, and
// returns herr without saving -- so the round stays queued and the next tick
// queues the same broken entry again: one duplicate per tick on a local OOM
// re-admit (oom.go).
//
// The state cannot be staged by hand: Admit refuses anything that is not
// State Active on entry, so it is produced by the switch's own resolve
// failure plus a failing delivery, exactly as it happens in production.
//
// Mutation check: narrow the condition in queue.go back to StateNeedsYou
// alone and the second Admit adds a second halt entry.
func TestAdmitKeepsAnAlreadyNotifiedBrokenHalt(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	// Two providers, so gating the first leaves the second reachable, and the
	// read tier the binding carries is one opencode refuses -- the resolve
	// failure switchBuilder reports as broken.
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"first","model":"m","roles":["builder","reviewer"]},
	  {"harness":"opencode","provider":"second","model":"m","roles":["builder"]}]`)
	rt.Policy = orderOf("builder", "claude/first/m", "opencode/second/m")
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/first/m", MasterMindID: testMasterMindName, CWD: "/repo", Tier: "read",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	stub := &failingConfirmDeliverer{}
	master, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rt.Deliverers = map[string]delivery.MasterMindDeliverer{master.MasterMind.Kind: stub}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/first/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	// The first Admit reaches the broken state and returns the delivery error.
	if err := Admit(context.Background(), rt, "webshop"); err == nil {
		t.Fatal("Admit = nil, want the delivery error returned")
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.State != store.StateBroken {
		t.Fatalf("state = %s, want broken: the fixture must reach the branch under test", b.State)
	}
	if b.HaltNotifiedRound != b.Round {
		t.Fatalf("HaltNotifiedRound = %d, want this round %d: the fixture must be already notified", b.HaltNotifiedRound, b.Round)
	}
	halts := haltEntries(t, rt, "webshop")
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want 1: the broken halt's own entry", len(halts))
	}

	// The next tick must add nothing. The daemon's idle path revives a broken
	// binding back to Active, which is what puts it in front of Admit again
	// with its stale key still stamped -- and that duplicate-per-tick is the
	// defect this test exists to pin.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		cur.State = store.StateActive
		cur.QueuedAt = baseTime
		return tx.Save(cur)
	}); err != nil {
		t.Fatalf("stage re-queue: %v", err)
	}
	if err := Admit(context.Background(), rt, "webshop"); err == nil {
		t.Fatal("second Admit = nil, want an error")
	}
	if halts := haltEntries(t, rt, "webshop"); len(halts) != 1 {
		t.Errorf("halt entries = %d after the second Admit, want 1: a duplicate was queued for a halt already notified", len(halts))
	}
}
