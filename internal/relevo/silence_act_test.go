package relevo

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// silentOpencode stages a two-candidate opencode round 30 minutes in whose
// model has been quiet for 12 minutes and whose tree last burned CPU cpuAgo
// ago, with session sess announced.
func silentOpencode(t *testing.T, fr *fakeRunner, sess string, cpuAgo time.Duration) (Runtime, store.Binding) {
	t.Helper()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 30*time.Minute)
	rt.CPUTicks = steadyCPU
	b.Builder.StreamSessionID = sess
	return rt, silenceStage(t, rt, b, cpuAgo)
}

// steadyCPU reads the tree's CPU as the staged sample left it, so the tree
// stays as idle as the stage says.
func steadyCPU(int) (uint64, bool) { return 5, true }

// silenceStage makes b's current process silent: a progress sample due again, a
// stream last written 12 minutes ago and the tree's last CPU burn cpuAgo ago.
func silenceStage(t *testing.T, rt Runtime, b store.Binding, cpuAgo time.Duration) store.Binding {
	t.Helper()
	now := rt.Now()
	quiet := 12 * time.Minute
	b.RoundStartedAt = now.Add(-30 * time.Minute)
	b.Progress = &store.Progress{
		SampledAt: now.Add(-time.Hour), TreeAt: b.RoundStartedAt, OutputAt: now.Add(-quiet),
		CPUTicks: 5, CPUTicksAt: now.Add(-cpuAgo),
	}
	stream := rt.Store.RunnerStreamPath(b.Name, b.Round)
	if err := os.WriteFile(stream, []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stream, now.Add(-quiet), now.Add(-quiet)); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFirstSilenceAbortsAndResumesSameSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := silentOpencode(t, fr, "ses_1", 20*time.Minute)
	switchesBefore := b.RoundSwitches

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(fr.kills) != 1 || len(fr.specs) != 1 || stallNotes(t, rt) != 1 {
		t.Fatalf("kills=%d specs=%d stall notes=%d after tick 1, want 1, 1, 1", len(fr.kills), len(fr.specs), stallNotes(t, rt))
	}

	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(fr.specs) != 2 || !containsArg(fr.specs[1].Argv, "--session", "ses_1") {
		t.Fatalf("specs = %d, want a resume of ses_1", len(fr.specs))
	}
	if got.BuilderCandidate != testOpencodeRef || got.RoundSwitches != switchesBefore || len(got.RoundExcluded) != 0 {
		t.Errorf("candidate=%q switches=%d excluded=%v, want same candidate, uncounted, none excluded", got.BuilderCandidate, got.RoundSwitches, got.RoundExcluded)
	}
	if n := sameSessionResumes(mustReadLog(t, rt), 1, "ses_1"); n != 1 {
		t.Errorf("same-session resumes = %d, want 1", n)
	}
	var resumed bool
	for _, e := range switches(t, rt) {
		resumed = resumed || strings.Contains(e.Note, "after provider stall: same candidate, not counted")
	}
	if !resumed {
		t.Errorf("switch entries = %+v, want a resume after provider stall", switches(t, rt))
	}
}

func TestSecondSilenceSwitchesCandidateUncounted(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := silentOpencode(t, fr, "ses_1", 20*time.Minute)
	appendResumeNote(t, rt, b, "provider stall")
	switchesBefore := b.RoundSwitches

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != switchesBefore {
		t.Errorf("candidate=%q switches=%d, want %q and %d", got.BuilderCandidate, got.RoundSwitches, testClaudeRef, switchesBefore)
	}
	if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testOpencodeRef {
		t.Errorf("RoundExcluded = %v, want [%s]", got.RoundExcluded, testOpencodeRef)
	}
	if len(fr.kills) != 1 {
		t.Errorf("kills = %d, want the outgoing process killed", len(fr.kills))
	}
}

func appendResumeNote(t *testing.T, rt Runtime, b store.Binding, cause string) {
	t.Helper()
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog(b.Name, store.LogEntry{
			TS: rt.Now(), Round: b.Round, Kind: store.KindSwitch, Confirmed: true,
			Note: sameSessionNotePrefix + " ses_1 of " + b.BuilderCandidate + " after " + cause + ": same candidate, not counted",
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSilenceAfterTransportResumeSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = transportExitOf(t, rt, b, "ses_1", transportErrLine)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("resume tick: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want the transport resume", len(fr.specs))
	}

	rt = at(rt, 40*time.Minute)
	rt.CPUTicks = steadyCPU
	got = silenceStage(t, rt, got, 20*time.Minute)
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("silent tick: %v", err)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 0 {
		t.Errorf("candidate=%q switches=%d, want a switch to %q, uncounted", got.BuilderCandidate, got.RoundSwitches, testClaudeRef)
	}
	if n := stallNotes(t, rt); n != 0 {
		t.Errorf("stall notes = %d, want none: the budget was spent", n)
	}
}

func TestSilenceDoesNotAbortWhileCPUBusy(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := silentOpencode(t, fr, "ses_1", time.Minute)
	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 0 || stallNotes(t, rt) != 0 {
		t.Errorf("kills=%d stall notes=%d with a busy CPU, want 0, 0", len(fr.kills), stallNotes(t, rt))
	}
}

func TestSilenceStopIsNotAUserStop(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := silentOpencode(t, fr, "ses_1", 20*time.Minute)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if !got.StopRequestedAt.IsZero() {
		t.Fatalf("StopRequestedAt = %s after the abort, want zero", got.StopRequestedAt)
	}
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if got.State != store.StateActive || got.RoundStartedAt.IsZero() || len(fr.specs) != 2 {
		t.Errorf("state=%s roundStarted=%s specs=%d, want the round still open and resumed", got.State, got.RoundStartedAt, len(fr.specs))
	}
}

func TestSilenceWithoutSessionIdSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := silentOpencode(t, fr, "", 20*time.Minute)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 0 || len(got.RoundExcluded) != 1 {
		t.Errorf("candidate=%q switches=%d excluded=%v, want an uncounted switch with the candidate excluded", got.BuilderCandidate, got.RoundSwitches, got.RoundExcluded)
	}
}
