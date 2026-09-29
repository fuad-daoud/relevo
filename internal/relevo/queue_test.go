package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
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
