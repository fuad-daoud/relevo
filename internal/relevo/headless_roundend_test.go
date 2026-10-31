package relevo

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// roundExitStream writes the round's stream with the parenthetical events a
// harness puts there, so a round's end is decided by the stream's own words
// rather than by the absence of an event.
func roundExitStream(t *testing.T, rt Runtime, b store.Binding, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	p := rt.Store.StreamPath(b.Name, b.Round)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// TestACutTurnAtCodeZeroSwitchesInsteadOfResuming pins the classifier's hold
// on the exit path: a claude round whose stream ends on an assistant message
// -- no result event following it -- is a turn cut off inside itself, so a
// code 0 exit is the runner-failure path and the session the turn left is not
// resumed: the builder is switched out, counted.
func TestACutTurnAtCodeZeroSwitchesInsteadOfResuming(t *testing.T) {
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
	roundExitStream(t, rt, b,
		`{"type":"system","subtype":"init","session_id":"S1"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"bash"}]}}`,
	)

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2: the send, then the switch a cut turn buys", len(fr.specs))
	}
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, nudgeNotePrefix) {
			t.Fatalf("a cut turn was nudged: %+v", e)
		}
	}
	if got.BuilderCandidate != testAgyRef || got.RoundSwitches != 1 {
		t.Errorf("candidate=%q switches=%d, want %q and 1 (counted failure switch)",
			got.BuilderCandidate, got.RoundSwitches, testAgyRef)
	}
	if got.RoundExcluded == nil || len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testClaudeRef {
		t.Errorf("RoundExcluded = %v, want the exited candidate excluded", got.RoundExcluded)
	}
}

// TestAStreamCutWithoutAnyTurnEventStillResumes pins the no-evidence rows:
// with no stream at all -- or a stream whose lines establish nothing -- the
// code gate keeps today's answer, and a zero exit with a session to resume is
// still resumed once.
func TestAStreamCutWithoutAnyTurnEventStillResumes(t *testing.T) {
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
	// The round's stream exists but carries neither a turn-end event nor a
	// mid-turn event its table knows: no verdict, today's nudge path.
	roundExitStream(t, rt, b, `{"type":"system","subtype":"init"}`)

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the resume): a stream with no verdict keeps the nudge", len(fr.specs))
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("candidate = %q, want the same candidate a nudge keeps", got.BuilderCandidate)
	}
}

// TestAStreamWithATurnEndEventAtCodeZeroStillResumes pins the clean arm's
// positive side: a stream whose last events include the harness's own
// end-of-turn event reads as a turn that ended, and the zero exit without a
// report is the resume it always was.
func TestAStreamWithATurnEndEventAtCodeZeroStillResumes(t *testing.T) {
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
	roundExitStream(t, rt, b,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"the work"}]}}`,
		`{"type":"result","result":"the work is drafted"}`,
	)

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the resume): a turn that ended cleanly is the resume its session earns", len(fr.specs))
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("candidate = %q, want the same candidate a nudge keeps", got.BuilderCandidate)
	}
}
