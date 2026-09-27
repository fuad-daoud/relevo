package relevo

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// staleClaudeOnlyJSON is a one-builder pool: dropping every other candidate
// leaves a binding on agy stale, with claude as the configured pick.
const staleClaudeOnlyJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
]`

// stalePoolJSON is a two-builder pool on provider "test": the set a binding
// re-picks from once its own candidate is gone.
const stalePoolJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`

// staleAgyPairJSON has two agy builders on different providers, so a rate
// limit on one leaves the other, and dropping one leaves the other as the
// stale token's near miss (same harness, different provider).
const staleAgyPairJSON = `[
  {"harness":"agy","provider":"old","model":"m","roles":["builder"]},
  {"harness":"agy","provider":"new","model":"m","roles":["builder"]}
]`

// staleAgyNewJSON is staleAgyPairJSON without agy/old/m.
const staleAgyNewJSON = `[
  {"harness":"agy","provider":"new","model":"m","roles":["builder"]}
]`

func TestStaleBuilder(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t) // testCandidatesJSON holds agy/test/m
	remote := store.Binding{
		BuilderCandidate: testAgyRef,
		Builder:          store.Endpoint{Mode: store.ModeRemote},
	}

	tests := []struct {
		name string
		set  *candidate.Set
		b    store.Binding
		want bool
	}{
		{"configured token", rt.Candidates, store.Binding{BuilderCandidate: testAgyRef}, false},
		{"unconfigured token", rt.Candidates, store.Binding{BuilderCandidate: "agy/other/m"}, true},
		{"empty token", rt.Candidates, store.Binding{}, false},
		{"remote binding", rt.Candidates, remote, false},
		{"nil set", nil, store.Binding{BuilderCandidate: testAgyRef}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := rt
			rt.Candidates = tt.set
			if got := staleBuilder(rt, tt.b); got != tt.want {
				t.Fatalf("staleBuilder = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGatedBuilderSeesAStaleTokensProvider(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, staleAgyPairJSON)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/old/m", time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	// The candidate was deleted: the set no longer holds agy/old/m.
	rt.Candidates = candidateSet(t, staleAgyNewJSON)

	if _, gated := gatedBuilder(rt, store.Binding{BuilderCandidate: "agy/old/m"}); !gated {
		t.Fatal("a stale token whose own provider is rate-limited is not gated")
	}
}

func TestGatedBuilderIgnoresTheEditedCandidatesNewProvider(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, staleAgyPairJSON)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/new/m", time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	rt.Candidates = candidateSet(t, staleAgyNewJSON)

	if _, gated := gatedBuilder(rt, store.Binding{BuilderCandidate: "agy/old/m"}); gated {
		t.Fatal("a gate on the edited candidate's new provider gated the process still on its old provider")
	}
}

func TestReconcileHeadlessStaleGatedSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	old := handleOf(b.Builder)

	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	// agy/other/m was deleted from the set after the round picked it.
	rt.Candidates = candidateSet(t, stalePoolJSON)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != old {
		t.Errorf("kills = %+v, want the stale builder's process %+v", fr.kills, old)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a claude replacement: %+v", fr.specs)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 0 || got.Builder.PID != fr.handles[1].PID {
		t.Errorf("bookkeeping: cand=%q switches=%d (want 0; a stale switch is uncounted) pid=%d", got.BuilderCandidate, got.RoundSwitches, got.Builder.PID)
	}
}

func TestLimitPatternsStaleTokenUsesHarnessPatterns(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, staleAgyNewJSON)

	h, ok := harness.Lookup("agy")
	if !ok || len(h.LimitPatterns) == 0 {
		t.Fatal("agy has no limit patterns to fall back to")
	}
	got := availability.LimitPatterns(AvailabilityDeps(rt), "agy/old/m")
	if len(got) != len(h.LimitPatterns) {
		t.Fatalf("limitPatterns on a stale token = %d patterns, want the harness's %d", len(got), len(h.LimitPatterns))
	}
}

func TestSendStaleBuilderPicksAgain(t *testing.T) {
	t.Parallel()

	rt, _ := switchSetup(t) // webshop bound on agy/test/m
	rt.Policy = orderOf("builder", testClaudeRef, testOpencodeRef)
	// agy/test/m was deleted from the set after the binding picked it.
	rt.Candidates = candidateSet(t, stalePoolJSON)

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# go"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.BuilderCandidate != testClaudeRef {
		t.Fatalf("BuilderCandidate = %q, want the configured pick %q", stored.BuilderCandidate, testClaudeRef)
	}
	if !strings.HasPrefix(res.Pick, "note: builder "+testAgyRef+" is no longer configured; ") {
		t.Fatalf("Pick = %q, want the stale-builder note", res.Pick)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var pick string
	for _, e := range entries {
		if e.Kind == store.KindPick {
			pick = e.Note
		}
	}
	if pick == "" || !strings.Contains(pick, testClaudeRef) {
		t.Fatalf("pick entry = %q, want a KindPick naming %s", pick, testClaudeRef)
	}
}

func TestSendStaleBuilderNoCandidateRefuses(t *testing.T) {
	t.Parallel()

	rt, _ := switchSetup(t)
	rt.Policy = orderOf("builder", testClaudeRef, testOpencodeRef)
	// The stale binding's pool, every member of which is gated.
	rt.Candidates = candidateSet(t, stalePoolJSON)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "# go"), SendOptions{})
	if err == nil || !strings.Contains(err.Error(), "no longer configured") {
		t.Fatalf("Send error = %v, want one naming the no-longer-configured builder", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Kind == store.KindPrompt {
			t.Fatalf("Send wrote a plan entry despite the refusal: %+v", e)
		}
	}
}

func TestAdmitStaleBuilderSwitches(t *testing.T) {
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
	// agy/other/m was deleted from the set while the round was queued.
	rt.Candidates = candidateSet(t, stalePoolJSON)

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
		t.Errorf("RoundSwitches = %d, want 0 (a stale switch is uncounted)", got.RoundSwitches)
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero", got.QueuedAt)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindQueue || !strings.Contains(last.Note, "no longer configured") {
		t.Fatalf("last entry = %+v, want a KindQueue note naming the no-longer-configured candidate", last)
	}
}

func TestRegateStaleBuilderPicksAgain(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b.Gate = "make check"
	b.Regate = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}

	// The round's marker closes it, and its gate fails.
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile (start gate): %v", err)
	}
	if got.GateRun == nil {
		t.Fatal("the gate did not start")
	}
	pid := got.GateRun.PID
	if err := os.WriteFile(rt.Store.GateLogPath("webshop", 1), []byte("FAIL github.com/example/pkg2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(pid, false)
	fr.exit(pid, 2)

	// agy/test/m was deleted from the set before the repair round's pick.
	rt.Candidates = candidateSet(t, staleClaudeOnlyJSON)

	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (close gate): %v", err)
	}
	if got.Round != 2 || got.State != store.StateActive {
		t.Fatalf("round=%d state=%q, want a repair round 2, active", got.Round, got.State)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Fatalf("BuilderCandidate = %q, want the configured pick %q", got.BuilderCandidate, testClaudeRef)
	}
	if got.RepairCount != 1 || got.LastGateSig == "" {
		t.Errorf("repairs=%d sig=%q, want 1 and a signature", got.RepairCount, got.LastGateSig)
	}
	if got.Builder.PID == 0 {
		t.Error("the repair round's process must be recorded on the binding")
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var pick string
	for _, e := range entries {
		if e.Kind == store.KindPick {
			pick = e.Note
		}
	}
	if !strings.Contains(pick, testClaudeRef) {
		t.Fatalf("pick entry = %q, want a KindPick naming %s", pick, testClaudeRef)
	}
}

func TestReconcileHeadlessLostToDaemonRestartStaleSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0) // the daemon started after the builder
	rt.Watched = NewWatched()                           // and it has seen nothing yet: this builder is lost
	fr.script(b.Builder.PID, false)                     // exited; no exit() set: code unknown
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})

	// agy/test/m was deleted from the set between the restart and this tick.
	rt.Candidates = candidateSet(t, staleClaudeOnlyJSON)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Fatalf("state = %q, want active: the stale lost builder must switch, not halt", got.State)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Fatalf("BuilderCandidate = %q, want the configured pick %q", got.BuilderCandidate, testClaudeRef)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a claude replacement: %+v", fr.specs)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var note string
	for _, e := range entries {
		if e.Kind == store.KindSwitch {
			note = e.Note
		}
	}
	if !strings.Contains(note, "no longer configured") {
		t.Fatalf("switch entry = %q, want it to name the no-longer-configured candidate", note)
	}
}

func TestGatedNoteStaleToken(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, staleAgyPairJSON)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/old/m", time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	rt.Candidates = candidateSet(t, staleAgyNewJSON)

	got := availability.GatedNote(AvailabilityDeps(rt), "agy/old/m")
	if got == "" {
		t.Fatal("a stale token whose provider is gated printed no note")
	}
	if !strings.Contains(got, "agy/old/m") {
		t.Fatalf("note = %q, want it to name the stale token", got)
	}
}
