package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// picks returns the pick entries in a binding's log, in order.
func picks(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%q): %v", name, err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindPick {
			out = append(out, e)
		}
	}
	return out
}

// kinds returns the kinds in a binding's log, in order, for position checks.
func kinds(t *testing.T, rt Runtime, name string) []store.Kind {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%q): %v", name, err)
	}
	out := make([]store.Kind, len(entries))
	for i, e := range entries {
		out[i] = e.Kind
	}
	return out
}

func TestBindPicksFirstUngatedInOrder(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	availability.RecordSpawnFailure(AvailabilityDeps(rt), testAgyRef, "earlier", errors.New("agent start: exit 1"))
	untilText := availability.GateUntilText(baseTime.Add(availability.SpawnFailedCooldown))

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if !b.Builder.Headless() || b.Builder.Kind != "claude" {
		t.Fatalf("Builder = %+v, want a headless claude endpoint", b.Builder)
	}
	if b.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", b.BuilderCandidate, testClaudeRef)
	}

	got := picks(t, rt, "webshop")
	if len(got) != 1 {
		t.Fatalf("picks = %+v, want 1", got)
	}
	p := got[0]
	if !p.Confirmed || p.Direction != store.DirToMasterMind || p.Round != 1 {
		t.Errorf("pick entry = %+v", p)
	}
	wantNote := "picked claude/test/m for builder: order #2; skipped agy/test/m (spawn failed " + untilText + ")"
	if p.Note != wantNote {
		t.Errorf("pick note = %q, want %q", p.Note, wantNote)
	}
}

func TestBindResolvedReturnsTheResolution(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	availability.RecordSpawnFailure(AvailabilityDeps(rt), testAgyRef, "earlier", errors.New("agent start: exit 1"))

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if res.How != HowOrder {
		t.Errorf("How = %q, want HowOrder", res.How)
	}
	if res.Position != 2 {
		t.Errorf("Position = %d, want 2", res.Position)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("Skipped = %+v, want len 1", res.Skipped)
	}
}

func TestBindRefusesWhenEveryCandidateIsGated(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	// testClaudeRef's provider ("test") is shared by all three candidates in
	// testCandidatesJSON, so this gates all three.
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testClaudeRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if !errors.Is(err, ErrAllGated) {
		t.Fatalf("err = %v, want ErrAllGated", err)
	}
	if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", err)
	}
}

func TestBindExplicitGatedBypassesAndLogsIt(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	availability.RecordSpawnFailure(AvailabilityDeps(rt), testAgyRef, "earlier", errors.New("agent start: exit 1"))
	untilText := availability.GateUntilText(baseTime.Add(availability.SpawnFailedCooldown))

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	got := picks(t, rt, "webshop")
	if len(got) != 1 {
		t.Fatalf("picks = %+v, want 1", got)
	}
	wantNote := "picked agy/test/m for builder: explicit, policy bypassed; gated: spawn failed " + untilText
	if got[0].Note != wantNote {
		t.Errorf("pick note = %q, want %q", got[0].Note, wantNote)
	}
}

// TestResumeRebindLogsPickAtCurrentRound mirrors
// TestResumeAllowsRebindWhenSessionlessBuilderPaneIsGone. seedBound's own
// initial Bind (Candidate: testAgyRef, explicit) already writes a leading
// pick entry, so this checks the entry the resume itself adds, not the
// total count.

// TestResumeRebindLogsPickAtCurrentRound mirrors
// TestResumeAllowsRebindWhenSessionlessBuilderPaneIsGone. seedBound's own
// initial Bind (Candidate: testAgyRef, explicit) already writes a leading
// pick entry, so this checks the entry the resume itself adds, not the
// total count.
func TestResumeRebindLogsPickAtCurrentRound(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)
	before := picks(t, rt, "webshop")

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo", Resume: true,
	})
	if err != nil {
		t.Fatalf("Bind resume: %v", err)
	}

	after := picks(t, rt, "webshop")
	if len(after) != len(before)+1 {
		t.Fatalf("picks after resume = %+v, want %d entries (one more than before)", after, len(before)+1)
	}
	got := after[len(after)-1]
	if got.Round != b.Round {
		t.Errorf("pick round = %d, want %d", got.Round, b.Round)
	}
	if !strings.HasPrefix(got.Note, "picked agy/test/m for builder: explicit") {
		t.Errorf("pick note = %q", got.Note)
	}
}

func TestAddLogsPick(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	rt.Policy = orderOf("builder", testAgyRef)

	res, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: "", MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Resolution.How != HowOrder {
		t.Errorf("Resolution.How = %q, want HowOrder", res.Resolution.How)
	}

	got := picks(t, rt, "frontend")
	if len(got) != 1 {
		t.Fatalf("picks = %+v, want 1", got)
	}
	if got[0].Round != 1 {
		t.Errorf("pick round = %d, want 1", got[0].Round)
	}
}
