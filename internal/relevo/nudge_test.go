package relevo

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// The nudge-resume fixtures build on headless_test.go's helpers (seedClaudeHeadless,
// sentHeadless, at, switches, containsArg) and the fake runner: an exited
// builder is driven purely through the script.

func TestExitZeroWithoutReportResumesSessionOnce(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	const sess = "S1"
	b.Builder.StreamSessionID = sess
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt = at(rt, time.Minute)
	keep := b.RoundStartedAt

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the resume)", len(fr.specs))
	}
	resume := fr.specs[1].Argv
	if resume[0] != "claude" {
		t.Errorf("resume argv[0] = %q, want claude", resume[0])
	}
	if !containsArg(resume, "--resume", sess) {
		t.Errorf("resume argv = %v, want --resume %s", resume, sess)
	}
	if !anyArgContains(resume, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("resume argv = %v, want its prompt to name the report path %s", resume, rt.Store.ReportPath("webshop", 1))
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q: a nudge keeps the same candidate", got.BuilderCandidate, testClaudeRef)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, nudgeNotePrefix) {
		t.Fatalf("switch entries = %+v, want one starting %q", sw, nudgeNotePrefix)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (not counted)", got.RoundSwitches)
	}
	if len(got.RoundExcluded) != 0 {
		t.Errorf("RoundExcluded = %v, want empty", got.RoundExcluded)
	}
	if !got.RoundStartedAt.Equal(keep) {
		t.Errorf("RoundStartedAt = %s, want the pre-nudge value %s: the nudge buys no time", got.RoundStartedAt, keep)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	if got.Builder.PID != fr.handles[1].PID {
		t.Errorf("PID = %d, want the resumed handle %d", got.Builder.PID, fr.handles[1].PID)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want none", got.Halt)
	}
}

func TestSecondExitAfterNudgeSwitchesAsBefore(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("after the first tick specs = %d, want 2 (the nudge)", len(fr.specs))
	}

	// The resumed process also ends its turn without a report, and announces
	// its own session: the once-per-plan check must still block a second nudge.
	fr.script(first.Builder.PID, false)
	fr.exit(first.Builder.PID, 0)
	first.Builder.StreamSessionID = "S2"
	if err := rt.Store.Save(first); err != nil {
		t.Fatal(err)
	}
	second, err := reconcile(t, at(rt, 2*time.Minute), first)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(fr.specs) != 3 {
		t.Fatalf("specs = %d, want 3 (one nudge, then a switch)", len(fr.specs))
	}
	if second.BuilderCandidate != testAgyRef || second.RoundSwitches != 1 {
		t.Errorf("after the second exit: candidate=%q switches=%d, want %q and 1 (a counted switch)",
			second.BuilderCandidate, second.RoundSwitches, testAgyRef)
	}
	var nudges int
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, nudgeNotePrefix) {
			nudges++
		}
	}
	if nudges != 1 {
		t.Errorf("nudge entries = %d, want 1: the once-per-plan check must block the second", nudges)
	}
}

func TestNonZeroExitIsNotNudged(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 1)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, nudgeNotePrefix) {
			t.Fatalf("a non-zero exit was nudged: %+v", e)
		}
	}
	if got.BuilderCandidate != testAgyRef || got.RoundSwitches != 1 {
		t.Errorf("candidate=%q switches=%d, want %q and 1 (today's counted switch)",
			got.BuilderCandidate, got.RoundSwitches, testAgyRef)
	}
}

func TestExitWithoutSessionIsNotNudged(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, nudgeNotePrefix) {
			t.Fatalf("a builder with no session was nudged: %+v", e)
		}
	}
	if got.BuilderCandidate != testAgyRef || got.RoundSwitches != 1 {
		t.Errorf("candidate=%q switches=%d, want %q and 1", got.BuilderCandidate, got.RoundSwitches, testAgyRef)
	}
}

func TestCodexExitIsNotNudged(t *testing.T) {
	t.Parallel()

	const codexCandidatesJSON = `[
	  {"harness":"codex","provider":"openai","model":"gpt-5.6-terra","roles":["builder"]}
	]`
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, codexCandidatesJSON)
	rt.Runner = fr
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "codex/openai/gpt-5.6-terra", MasterMindID: testMasterMindName,
		CWD: "/repo", Headless: true, Tier: "edit",
	}); err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.StreamSessionID = "codex-thread-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcile(t, at(rt, time.Minute), b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Errorf("specs = %d, want 1: codex has no resume form, so no nudge process starts", len(fr.specs))
	}
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, nudgeNotePrefix) {
			t.Fatalf("a codex exit was nudged: %+v", e)
		}
	}
}

func TestResendAllowsAnotherNudge(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("after the first tick specs = %d, want 2 (the nudge)", len(fr.specs))
	}

	// A resend of the same round: a newer plan entry allows another nudge.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("webshop", store.LogEntry{
			TS: rt.Now().UTC(), Round: 1, Direction: store.DirToBuilder,
			Kind: store.KindPlan, Confirmed: true, Note: "resend",
		})
	}); err != nil {
		t.Fatalf("append resend plan: %v", err)
	}
	first.Builder.StreamSessionID = "S2"
	if err := rt.Store.Save(first); err != nil {
		t.Fatal(err)
	}
	fr.script(first.Builder.PID, false)
	fr.exit(first.Builder.PID, 0)
	second, err := reconcile(t, at(rt, 2*time.Minute), first)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(fr.specs) != 3 {
		t.Fatalf("specs = %d, want 3 (a second nudge)", len(fr.specs))
	}
	var nudges int
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, nudgeNotePrefix) {
			nudges++
		}
	}
	if nudges != 2 {
		t.Errorf("nudge entries = %d, want 2: a resend allows another nudge", nudges)
	}
	if second.RoundSwitches != 0 || second.BuilderCandidate != testClaudeRef {
		t.Errorf("candidate=%q switches=%d, want %q and 0", second.BuilderCandidate, second.RoundSwitches, testClaudeRef)
	}
}

func TestLimitExitIsGatedNotNudged(t *testing.T) {
	t.Parallel()

	const otherRef = "agy/other/m"
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", testClaudeRef, otherRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testClaudeRef, MasterMindID: testMasterMindName,
		CWD: "/repo", Headless: true, Tier: "edit",
	}); err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\nusage limit reached\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || strings.HasPrefix(sw[0].Note, nudgeNotePrefix) {
		t.Fatalf("switch entries = %+v, want one rate-limited switch and no nudge", sw)
	}
	if !strings.Contains(sw[0].Note, "rate-limited") {
		t.Errorf("switch note = %q, want the rate-limited gate", sw[0].Note)
	}
	if got.BuilderCandidate != otherRef || got.RoundSwitches != 0 {
		t.Errorf("candidate=%q switches=%d, want %q and 0 (the gate is uncounted)",
			got.BuilderCandidate, got.RoundSwitches, otherRef)
	}
}
