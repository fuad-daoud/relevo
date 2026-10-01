package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// scopeOOM is the journal answer for an oom-killed round: the incident's
// result and the 8178532352 bytes (7.6 GiB) its MEMORY_PEAK carried.
var scopeOOM = spawn.ScopeResult{Result: scopeResultOOM, PeakBytes: 8178532352}

// oomRT adds scopes and a scripted scope result to rt so oomKilled returns the
// expected value. It is a test-local helper: no caller may replace rt.Runner.
func oomRT(rt Runtime, fr *fakeRunner, b store.Binding, result spawn.ScopeResult) Runtime {
	rt.Scope = &spawn.ScopeSpec{Unit: "test", Slice: "relevo.slice", CPUWeight: 100}
	unit := scopeUnitName(b)
	if fr.scopeResults == nil {
		fr.scopeResults = map[string]spawn.ScopeResult{}
	}
	fr.scopeResults[unit] = result
	return rt
}

// TestOOMKilledLocalRoundBecomesQueued pins the primary oom path: when a local
// round's builder exits with code unknown and the scope result is "oom-kill",
// the round is re-queued on the same candidate, not switched.
//
// Pins: QueuedAt set, RoundStartedAt zero, OOMRequeue.Running set, RoundSwitches
// unchanged, RoundExcluded empty, BuilderCandidate the same, queue log entry
// written, exit entry note says the cgroup scope ran out of memory.
func TestOOMKilledLocalRoundBecomesQueued(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	wantCandidate := b.BuilderCandidate
	fr.script(b.Builder.PID, false) // exited; no exit() set: code unknown
	rt = oomRT(rt, fr, b, scopeOOM)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.Builder.PID != 0 {
		t.Errorf("PID = %d, want 0 (process gone)", got.Builder.PID)
	}
	if got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = zero, want non-zero (queued)")
	}
	if !got.RoundStartedAt.IsZero() {
		t.Errorf("RoundStartedAt = %v, want zero", got.RoundStartedAt)
	}
	if got.OOMRequeue == nil {
		t.Fatalf("OOMRequeue = nil, want non-nil")
	}
	if got.OOMRequeue.Running != 1 {
		// Only the killed round was running (no other local rounds).
		t.Errorf("OOMRequeue.Running = %d, want 1", got.OOMRequeue.Running)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (not counted)", got.RoundSwitches)
	}
	if len(got.RoundExcluded) != 0 {
		t.Errorf("RoundExcluded = %v, want empty", got.RoundExcluded)
	}
	if got.BuilderCandidate != wantCandidate {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, wantCandidate)
	}

	// Exactly one exit entry, note mentions oom.
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1", len(ex))
	}
	if !strings.Contains(ex[0].Note, "killed: out of memory") {
		t.Errorf("exit note = %q, want it to name the cgroup oom kill", ex[0].Note)
	}
	if strings.Contains(ex[0].Note, "systemd-oomd") || strings.Contains(ex[0].Note, "host out of memory") {
		t.Errorf("exit note = %q, want no host-wide systemd-oomd claim", ex[0].Note)
	}

	// Exactly one queue entry.
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var queueEntries []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindQueue {
			queueEntries = append(queueEntries, e)
		}
	}
	if len(queueEntries) != 1 || !strings.Contains(queueEntries[0].Note, "out of memory") {
		t.Errorf("queue entries = %+v, want one naming the oom kill", queueEntries)
	}

	// No switch entry.
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %v, want none", sw)
	}

	// No second process started.
	if len(fr.specs) != 1 {
		t.Errorf("specs = %d, want 1 (no new start)", len(fr.specs))
	}
}

// TestOOMKilledOpenCodeSessionIsAbandoned pins that abandonSession is called
// during an oom kill: the killed round's session appears in AbandonedSessions.
func TestOOMKilledOpenCodeSessionIsAbandoned(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	// opencode is the one harness relevo can delete sessions for, so it is the
	// only kind for which abandonSession records an entry.
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	const sess = "sess-oom-killed"
	b.Builder.StreamSessionID = sess
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false) // exited; code unknown
	rt = oomRT(rt, fr, b, scopeOOM)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	found := false
	for _, s := range got.AbandonedSessions {
		if s.ID == sess {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("AbandonedSessions = %+v, want it to contain session %q", got.AbandonedSessions, sess)
	}
}

// TestOOMKilledServedRoundIsQueued pins that a served round (b.Owner != "")
// is re-queued on oom kill the same way a local round is.
func TestOOMKilledServedRoundIsQueued(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	b.Owner = "owner-served"
	wantQueuedAt := b.RoundStartedAt
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	rt = oomRT(rt, fr, b, scopeOOM)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.Builder.PID != 0 {
		t.Errorf("PID = %d, want 0", got.Builder.PID)
	}
	if got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = zero, want non-zero (queued)")
	}
	if !got.QueuedAt.Equal(wantQueuedAt) {
		t.Errorf("QueuedAt = %v, want old RoundStartedAt %v", got.QueuedAt, wantQueuedAt)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
}

// TestOOMKilledThirdKillHalts pins that the oomMaxKills-th oom kill in a round
// halts with NEEDS YOU instead of re-queuing.
func TestOOMKilledThirdKillHalts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	b.RoundOOMKills = oomMaxKills - 1 // one kill away from the limit
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	rt = oomRT(rt, fr, b, scopeOOM)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you on the %d-th oom kill", got.State, oomMaxKills)
	}
	if !strings.Contains(got.Halt, "out of memory") {
		t.Errorf("Halt = %q, want it to mention out of memory", got.Halt)
	}
}

// TestOOMKilledStopRequestedAtWins pins that a requested stop still closes the
// round even when the scope result is oom-kill: StopRequestedAt branch runs
// before the oom branch.
func TestOOMKilledStopRequestedAtWins(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	b.StopRequestedAt = baseTime
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	rt = oomRT(rt, fr, b, scopeOOM)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Closed as stopped: not queued, not NEEDS YOU.
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero (stopped, not queued)", got.QueuedAt)
	}
	if got.State == store.StateNeedsYou {
		t.Errorf("state = needs_you; a requested stop must not halt")
	}
}

// TestOOMKilledSuccessResultTakesNormalPath pins that a scope result of
// "success" still takes today's path (switch to next candidate), not the oom
// branch.
//
// Mutation check (step 7): make oomKilled always return true and this test
// fails on the binding not switching (becoming queued instead).
func TestOOMKilledSuccessResultTakesNormalPath(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false) // exited; code unknown
	rt = oomRT(rt, fr, b, spawn.ScopeResult{Result: "success"})

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Must switch, not queue.
	if got.QueuedAt.IsZero() == false {
		t.Errorf("QueuedAt = %v, want zero (should switch, not queue)", got.QueuedAt)
	}
	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Errorf("switch entries = %d, want 1 (normal exit path)", len(sw))
	}
	if len(fr.specs) != 2 {
		t.Errorf("specs = %d, want 2 (the switch)", len(fr.specs))
	}
}

// TestAdmitOOMQueuedRoundPutsNoteInPrompt pins that Admit of an oom-queued
// round injects oomNote text in the spawned prompt and clears OOMRequeue.
func TestAdmitOOMQueuedRoundPutsNoteInPrompt(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}

	// Patch the binding to look like an oom-queued round.
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	killTime := baseTime.Add(-2 * time.Minute)
	b.OOMRequeue = &store.OOMRequeue{At: killTime, Running: 1}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Admit(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	// The prompt must contain the oom note text.
	prompt := ""
	for _, arg := range fr.specs[0].Argv {
		if strings.Contains(arg, "out of memory") {
			prompt = arg
			break
		}
	}
	if prompt == "" {
		t.Errorf("no prompt arg contains oom note; argv = %v", fr.specs[0].Argv)
	}
	if !strings.Contains(prompt, "git status") {
		t.Errorf("oom note in prompt missing %q: %q", "git status", prompt)
	}

	// OOMRequeue must be cleared.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load after Admit: %v", err)
	}
	if got.OOMRequeue != nil {
		t.Errorf("OOMRequeue = %+v, want nil (cleared by Admit)", got.OOMRequeue)
	}
}

// TestTickAdmitsOOMQueuedPastCooldown pins the daemon Tick phase: a local
// oom-queued binding past the cooldown, with no other running rounds, is
// started by Tick. Before the cooldown, it is not.
//
// Mutation check (step 7): drop the running < b.OOMRequeue.Running condition
// and TestAdmitOOMQueuedWaitsForLowerRunning fails.
func TestTickAdmitsOOMQueuedPastCooldown(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}

	// Make the binding look oom-queued.
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	killTime := baseTime
	b.OOMRequeue = &store.OOMRequeue{At: killTime, Running: 1}
	b.QueuedAt = killTime
	b.RoundStartedAt = time.Time{}
	b.Builder.PID = 0
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Before cooldown: Tick must not admit.
	preCooldown := at(rt, oomCooldown-time.Second)
	if err := NewDaemon(preCooldown, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick (pre-cooldown): %v", err)
	}
	bAfterPre, _ := rt.Store.Load("webshop")
	if bAfterPre.Builder.PID != 0 {
		t.Errorf("admitted before cooldown: PID = %d, want 0", bAfterPre.Builder.PID)
	}

	// After cooldown: Tick must admit (running=0 < Running=1).
	postCooldown := at(rt, oomCooldown+time.Second)
	if err := NewDaemon(postCooldown, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick (post-cooldown): %v", err)
	}
	bAfterPost, _ := rt.Store.Load("webshop")
	if bAfterPost.Builder.PID == 0 {
		t.Errorf("not admitted after cooldown: want PID != 0")
	}
}

// TestOOMKilledSIGKILLExitIsQueued pins that the shell supervisor's 137
// (128+SIGKILL, written when the kernel kills only the inner process) is probed
// like an unknown exit: with the journal's oom-kill it re-queues the same
// candidate.
func TestOOMKilledSIGKILLExitIsQueued(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	wantCandidate := b.BuilderCandidate
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 137)
	rt = oomRT(rt, fr, b, scopeOOM)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.OOMRequeue == nil {
		t.Fatalf("OOMRequeue = nil, want the round re-queued on 137 + journal oom-kill")
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (not counted)", got.RoundSwitches)
	}
	if got.BuilderCandidate != wantCandidate {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, wantCandidate)
	}
	if len(fr.specs) != 1 {
		t.Errorf("specs = %d, want 1 (no new start)", len(fr.specs))
	}
}

// TestOOMKilledJournalFailureTakesTodayPath pins that a failing probe is not an
// oom kill: the round switches exactly as it did before.
func TestOOMKilledJournalFailureTakesTodayPath(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false) // exited; code unknown
	rt = oomRT(rt, fr, b, spawn.ScopeResult{})
	fr.scopeResultErr = errors.New("journal read failed")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.OOMRequeue != nil {
		t.Errorf("OOMRequeue = %+v, want nil (a probe failure is not an oom kill)", got.OOMRequeue)
	}
	if sw := switches(t, rt); len(sw) != 1 {
		t.Errorf("switch entries = %d, want 1 (today's path)", len(sw))
	}
}

// TestOOMKilledJournalWithoutRecordTakesTodayPath pins that a readable journal
// with no entry for the unit is not an oom kill: the round switches.
func TestOOMKilledJournalWithoutRecordTakesTodayPath(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	rt.Scope = &spawn.ScopeSpec{Unit: "test", Slice: "relevo.slice", CPUWeight: 100}
	// No scopeResults entry for the unit: the fake answers with the zero result.

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.OOMRequeue != nil {
		t.Errorf("OOMRequeue = %+v, want nil (an empty journal is not an oom kill)", got.OOMRequeue)
	}
	if sw := switches(t, rt); len(sw) != 1 {
		t.Errorf("switch entries = %d, want 1 (today's path)", len(sw))
	}
}

// TestOOMKilledExitNoteNamesThePeak pins that the exit entry's suffix carries
// the human peak the journal reported.
func TestOOMKilledExitNoteNamesThePeak(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	rt = oomRT(rt, fr, b, scopeOOM)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 || !strings.Contains(ex[0].Note, "peak 7.6 GiB") {
		t.Errorf("exit entries = %+v, want one naming the 7.6 GiB peak", ex)
	}
}

// TestOOMKilledRequeueNoteNamesThePeak pins that the local re-queue note carries
// the human peak the journal reported.
func TestOOMKilledRequeueNoteNamesThePeak(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	rt = oomRT(rt, fr, b, scopeOOM)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindQueue && strings.Contains(e.Note, "peak 7.6 GiB") {
			found = true
		}
	}
	if !found {
		t.Errorf("queue entries = %+v, want one naming the 7.6 GiB peak", entries)
	}
}

// TestOOMKilledThirdKillHaltNamesThePeak pins that the oomMaxKills halt carries
// the human peak the journal reported.
func TestOOMKilledThirdKillHaltNamesThePeak(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	b.RoundOOMKills = oomMaxKills - 1 // one kill away from the limit
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	rt = oomRT(rt, fr, b, scopeOOM)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you on the %d-th oom kill", got.State, oomMaxKills)
	}
	if !strings.Contains(got.Halt, "peak 7.6 GiB") {
		t.Errorf("Halt = %q, want it to name the 7.6 GiB peak", got.Halt)
	}
	if strings.Contains(got.Halt, "systemd-oomd") {
		t.Errorf("Halt = %q, want no host-wide systemd-oomd claim", got.Halt)
	}
}
